// Package memory is the assistant's persistent memory, in two layers that
// are both scoped by account and user.
//
// Notes are files the model manages itself through the Anthropic memory
// tool's commands (view, create, str_replace, insert, delete, rename) over
// paths under /memories. They live in the host's store, keyed by account,
// user, and path.
//
// Recall is retrieval over facts: Remember stores a fact in the account's
// "memory" collection of a rag.Index tagged with the user; Recall ranks
// by vector similarity times a recency factor with a 30-day half-life and
// skips tombstoned facts; Preamble formats the top facts for a prompt.
package memory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/bboxsys/platform/ai"
	"github.com/bboxsys/store"
	"github.com/dgraph-io/badger/v4"
)

const (
	// NotesPrefix is the store prefix notes are kept under, scoped by
	// account; the key is "{userID}:{path}".
	NotesPrefix = "ai_memory"
	// NotesRoot is the directory every note path must be under.
	NotesRoot = "/memories"
	// MaxPathBytes bounds a note path.
	MaxPathBytes = 256
	// MaxFileBytes bounds a note's content.
	MaxFileBytes = 64 * 1024
)

// note is the stored record.
type note struct {
	UserID    string    `json:"user_id"`
	Path      string    `json:"path"`
	Content   string    `json:"content"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Notes is the model-managed file memory.
type Notes struct {
	st *store.Store
}

// NewNotes returns notes backed by st.
func NewNotes(st *store.Store) *Notes {
	return &Notes{st: st}
}

// ErrNotFound is returned by Read for a path that has no note.
var ErrNotFound = errors.New("memory: not found")

// NormalizePath cleans p and checks that it is an absolute path under
// NotesRoot with no ".." segment and at most MaxPathBytes bytes. It
// returns the clean path (NotesRoot itself for the root directory).
func NormalizePath(p string) (string, error) {
	if p == "" {
		return "", errors.New("memory: path is empty")
	}
	if len(p) > MaxPathBytes {
		return "", fmt.Errorf("memory: path longer than %d bytes", MaxPathBytes)
	}
	if strings.ContainsAny(p, "\x00\n\r") {
		return "", errors.New("memory: path contains control characters")
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." {
			return "", fmt.Errorf("memory: path %q escapes %s", p, NotesRoot)
		}
	}
	clean := path.Clean(p)
	if clean != NotesRoot && !strings.HasPrefix(clean, NotesRoot+"/") {
		return "", fmt.Errorf("memory: path %q must be under %s", p, NotesRoot)
	}
	return clean, nil
}

func checkIdentity(accountID, userID string) error {
	if accountID == "" || strings.Contains(accountID, ":") {
		return fmt.Errorf("memory: invalid account id %q", accountID)
	}
	if userID == "" || strings.Contains(userID, ":") {
		return fmt.Errorf("memory: invalid user id %q", userID)
	}
	return nil
}

func noteKey(userID, p string) string {
	return userID + ":" + p
}

// userPrefix is the raw store prefix of one user's notes in an account.
func userPrefix(accountID, userID string) string {
	return store.ScopedPrefix(accountID, NotesPrefix) + userID + ":"
}

// list returns the user's notes sorted by path.
func (n *Notes) list(accountID, userID string) ([]note, error) {
	if err := checkIdentity(accountID, userID); err != nil {
		return nil, err
	}
	var notes []note
	if err := n.st.Get(userPrefix(accountID, userID), &notes); err != nil {
		return nil, fmt.Errorf("memory: list notes: %w", err)
	}
	sort.Slice(notes, func(i, j int) bool { return notes[i].Path < notes[j].Path })
	return notes, nil
}

func (n *Notes) get(accountID, userID, p string) (note, bool, error) {
	var nt note
	err := n.st.GetScoped(accountID, NotesPrefix, noteKey(userID, p), &nt)
	if errors.Is(err, badger.ErrKeyNotFound) {
		return note{}, false, nil
	}
	if err != nil {
		return note{}, false, fmt.Errorf("memory: read %s: %w", p, err)
	}
	return nt, true, nil
}

func (n *Notes) put(accountID, userID, p, content string) error {
	if len(content) > MaxFileBytes {
		return fmt.Errorf("memory: %s would be %d bytes, more than the %d byte limit", p, len(content), MaxFileBytes)
	}
	nt := note{UserID: userID, Path: p, Content: content, UpdatedAt: time.Now().UTC()}
	if err := n.st.PutScoped(accountID, NotesPrefix, noteKey(userID, p), nt); err != nil {
		return fmt.Errorf("memory: write %s: %w", p, err)
	}
	return nil
}

func (n *Notes) remove(accountID, userID, p string) error {
	if err := n.st.DeleteScoped(accountID, NotesPrefix, noteKey(userID, p)); err != nil {
		return fmt.Errorf("memory: delete %s: %w", p, err)
	}
	return nil
}

// List returns the paths of the user's notes, sorted.
func (n *Notes) List(_ context.Context, accountID, userID string) ([]string, error) {
	notes, err := n.list(accountID, userID)
	if err != nil {
		return nil, err
	}
	paths := make([]string, 0, len(notes))
	for _, nt := range notes {
		paths = append(paths, nt.Path)
	}
	return paths, nil
}

// Read returns the content of the note at p, or ErrNotFound.
func (n *Notes) Read(_ context.Context, accountID, userID, p string) (string, error) {
	if err := checkIdentity(accountID, userID); err != nil {
		return "", err
	}
	clean, err := NormalizePath(p)
	if err != nil {
		return "", err
	}
	nt, ok, err := n.get(accountID, userID, clean)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("%w: %s", ErrNotFound, clean)
	}
	return nt.Content, nil
}

// command is the memory tool's input.
type command struct {
	Command    string `json:"command"`
	Path       string `json:"path"`
	FileText   string `json:"file_text"`
	OldStr     string `json:"old_str"`
	NewStr     string `json:"new_str"`
	InsertLine *int   `json:"insert_line"`
	InsertText string `json:"insert_text"`
	OldPath    string `json:"old_path"`
	NewPath    string `json:"new_path"`
	ViewRange  []int  `json:"view_range"`
}

// Execute runs one memory tool command for the user and returns what the
// model should read. Mistakes the model can correct (a missing file, a
// bad path, an ambiguous str_replace) come back as an error Result; a
// store failure is returned as an error.
//
// The commands follow the Anthropic memory_20250818 tool: view lists a
// directory or shows a file with line numbers (optionally a view_range of
// 1-based inclusive lines, -1 for the end); create writes a file;
// str_replace replaces exactly one occurrence of old_str; insert puts
// insert_text after line insert_line (0 for the top); delete removes a
// file or a directory subtree; rename moves a file or a directory.
func (n *Notes) Execute(ctx context.Context, accountID, userID string, input json.RawMessage) (ai.Result, error) {
	if err := checkIdentity(accountID, userID); err != nil {
		return ai.Result{}, err
	}
	var cmd command
	if err := json.Unmarshal(input, &cmd); err != nil {
		return ai.ErrorResult("memory: invalid input: %v", err), nil
	}
	switch cmd.Command {
	case "view":
		return n.view(accountID, userID, cmd)
	case "create":
		return n.create(accountID, userID, cmd)
	case "str_replace":
		return n.strReplace(accountID, userID, cmd)
	case "insert":
		return n.insert(accountID, userID, cmd)
	case "delete":
		return n.delete(accountID, userID, cmd)
	case "rename":
		return n.rename(accountID, userID, cmd)
	case "":
		return ai.ErrorResult("memory: command is required (view, create, str_replace, insert, delete, rename)"), nil
	default:
		return ai.ErrorResult("memory: unknown command %q (expected view, create, str_replace, insert, delete, rename)", cmd.Command), nil
	}
}

// under reports whether p is inside directory dir.
func under(dir, p string) bool {
	return strings.HasPrefix(p, dir+"/")
}

func (n *Notes) view(accountID, userID string, cmd command) (ai.Result, error) {
	p, err := NormalizePath(cmd.Path)
	if err != nil {
		return ai.ErrorResult("%v", err), nil
	}
	if p != NotesRoot {
		nt, ok, err := n.get(accountID, userID, p)
		if err != nil {
			return ai.Result{}, err
		}
		if ok {
			return viewFile(nt, cmd.ViewRange)
		}
	}
	notes, err := n.list(accountID, userID)
	if err != nil {
		return ai.Result{}, err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Here're the files and directories up to 2 levels deep in %s, excluding hidden items:\n", p)
	found := false
	seen := map[string]bool{}
	for _, nt := range notes {
		if !under(p, nt.Path) {
			continue
		}
		found = true
		rel := strings.TrimPrefix(nt.Path, p+"/")
		parts := strings.Split(rel, "/")
		// Up to two levels: a direct child, or a child of a subdirectory.
		// Deeper files are shown through their second-level directory.
		switch {
		case len(parts) == 1:
			b.WriteString(nt.Path + "\n")
		case len(parts) == 2:
			dir := p + "/" + parts[0]
			if !seen[dir] {
				seen[dir] = true
				b.WriteString(dir + "/\n")
			}
			b.WriteString(nt.Path + "\n")
		default:
			dir := p + "/" + parts[0]
			if !seen[dir] {
				seen[dir] = true
				b.WriteString(dir + "/\n")
			}
			sub := dir + "/" + parts[1]
			if !seen[sub] {
				seen[sub] = true
				b.WriteString(sub + "/\n")
			}
		}
	}
	if !found {
		if p == NotesRoot {
			b.WriteString("(empty)\n")
			return ai.Result{Content: b.String()}, nil
		}
		return ai.ErrorResult("memory: %s does not exist", p), nil
	}
	return ai.Result{Content: b.String()}, nil
}

// splitLines splits content into lines without a trailing empty line for
// a final newline, and reports whether there was one.
func splitLines(content string) ([]string, bool) {
	if content == "" {
		return nil, false
	}
	trailing := strings.HasSuffix(content, "\n")
	content = strings.TrimSuffix(content, "\n")
	return strings.Split(content, "\n"), trailing
}

func joinLines(lines []string, trailing bool) string {
	s := strings.Join(lines, "\n")
	if trailing && s != "" {
		s += "\n"
	}
	return s
}

func viewFile(nt note, viewRange []int) (ai.Result, error) {
	lines, _ := splitLines(nt.Content)
	start, end := 1, len(lines)
	if len(viewRange) > 0 {
		if len(viewRange) != 2 {
			return ai.ErrorResult("memory: view_range must be [start, end]"), nil
		}
		start, end = viewRange[0], viewRange[1]
		if end == -1 {
			end = len(lines)
		}
		if start < 1 || (len(lines) > 0 && start > len(lines)) {
			return ai.ErrorResult("memory: view_range start %d is outside 1..%d", start, len(lines)), nil
		}
		if end < start || end > len(lines) {
			return ai.ErrorResult("memory: view_range end %d is outside %d..%d", end, start, len(lines)), nil
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Here's the content of %s with line numbers:\n", nt.Path)
	for i := start; i <= end && i <= len(lines); i++ {
		fmt.Fprintf(&b, "%6d\t%s\n", i, lines[i-1])
	}
	return ai.Result{Content: b.String()}, nil
}

func (n *Notes) create(accountID, userID string, cmd command) (ai.Result, error) {
	p, err := NormalizePath(cmd.Path)
	if err != nil {
		return ai.ErrorResult("%v", err), nil
	}
	if p == NotesRoot {
		return ai.ErrorResult("memory: %s is a directory", p), nil
	}
	if err := n.ensureNotDirectory(accountID, userID, p); err != nil {
		return ai.ErrorResult("%v", err), nil
	}
	if len(cmd.FileText) > MaxFileBytes {
		return ai.ErrorResult("memory: file_text is %d bytes, more than the %d byte limit", len(cmd.FileText), MaxFileBytes), nil
	}
	if err := n.put(accountID, userID, p, cmd.FileText); err != nil {
		return ai.Result{}, err
	}
	return ai.Result{Content: "File created successfully at: " + p}, nil
}

// ensureNotDirectory rejects a file path that is already a directory (a
// note exists below it) or that passes through an existing file.
func (n *Notes) ensureNotDirectory(accountID, userID, p string) error {
	notes, err := n.list(accountID, userID)
	if err != nil {
		return err
	}
	for _, nt := range notes {
		if under(p, nt.Path) {
			return fmt.Errorf("memory: %s is a directory", p)
		}
		if nt.Path != p && under(nt.Path, p) {
			return fmt.Errorf("memory: %s is a file, not a directory", nt.Path)
		}
	}
	return nil
}

func (n *Notes) strReplace(accountID, userID string, cmd command) (ai.Result, error) {
	p, err := NormalizePath(cmd.Path)
	if err != nil {
		return ai.ErrorResult("%v", err), nil
	}
	if cmd.OldStr == "" {
		return ai.ErrorResult("memory: old_str is required"), nil
	}
	nt, ok, err := n.get(accountID, userID, p)
	if err != nil {
		return ai.Result{}, err
	}
	if !ok {
		return ai.ErrorResult("memory: %s does not exist", p), nil
	}
	switch count := strings.Count(nt.Content, cmd.OldStr); {
	case count == 0:
		return ai.ErrorResult("memory: no replacement was performed, old_str did not appear verbatim in %s", p), nil
	case count > 1:
		return ai.ErrorResult("memory: no replacement was performed, old_str appears %d times in %s; include more context so it is unique", count, p), nil
	}
	content := strings.Replace(nt.Content, cmd.OldStr, cmd.NewStr, 1)
	if len(content) > MaxFileBytes {
		return ai.ErrorResult("memory: %s would exceed the %d byte limit", p, MaxFileBytes), nil
	}
	if err := n.put(accountID, userID, p, content); err != nil {
		return ai.Result{}, err
	}
	return ai.Result{Content: "The memory file has been edited."}, nil
}

func (n *Notes) insert(accountID, userID string, cmd command) (ai.Result, error) {
	p, err := NormalizePath(cmd.Path)
	if err != nil {
		return ai.ErrorResult("%v", err), nil
	}
	if cmd.InsertLine == nil {
		return ai.ErrorResult("memory: insert_line is required"), nil
	}
	nt, ok, err := n.get(accountID, userID, p)
	if err != nil {
		return ai.Result{}, err
	}
	if !ok {
		return ai.ErrorResult("memory: %s does not exist", p), nil
	}
	lines, trailing := splitLines(nt.Content)
	at := *cmd.InsertLine
	if at < 0 || at > len(lines) {
		return ai.ErrorResult("memory: insert_line %d is outside 0..%d", at, len(lines)), nil
	}
	newLines, _ := splitLines(cmd.InsertText)
	merged := make([]string, 0, len(lines)+len(newLines))
	merged = append(merged, lines[:at]...)
	merged = append(merged, newLines...)
	merged = append(merged, lines[at:]...)
	content := joinLines(merged, trailing || len(lines) == 0)
	if len(content) > MaxFileBytes {
		return ai.ErrorResult("memory: %s would exceed the %d byte limit", p, MaxFileBytes), nil
	}
	if err := n.put(accountID, userID, p, content); err != nil {
		return ai.Result{}, err
	}
	return ai.Result{Content: "The memory file has been edited."}, nil
}

func (n *Notes) delete(accountID, userID string, cmd command) (ai.Result, error) {
	p, err := NormalizePath(cmd.Path)
	if err != nil {
		return ai.ErrorResult("%v", err), nil
	}
	if p != NotesRoot {
		_, ok, err := n.get(accountID, userID, p)
		if err != nil {
			return ai.Result{}, err
		}
		if ok {
			if err := n.remove(accountID, userID, p); err != nil {
				return ai.Result{}, err
			}
			return ai.Result{Content: "Successfully deleted " + p}, nil
		}
	}
	notes, err := n.list(accountID, userID)
	if err != nil {
		return ai.Result{}, err
	}
	removed := 0
	for _, nt := range notes {
		if !under(p, nt.Path) {
			continue
		}
		if err := n.remove(accountID, userID, nt.Path); err != nil {
			return ai.Result{}, err
		}
		removed++
	}
	if removed == 0 && p != NotesRoot {
		return ai.ErrorResult("memory: %s does not exist", p), nil
	}
	return ai.Result{Content: "Successfully deleted " + p}, nil
}

func (n *Notes) rename(accountID, userID string, cmd command) (ai.Result, error) {
	from, err := NormalizePath(cmd.OldPath)
	if err != nil {
		return ai.ErrorResult("old_path: %v", err), nil
	}
	to, err := NormalizePath(cmd.NewPath)
	if err != nil {
		return ai.ErrorResult("new_path: %v", err), nil
	}
	if from == NotesRoot || to == NotesRoot {
		return ai.ErrorResult("memory: cannot rename %s", NotesRoot), nil
	}
	if from == to {
		return ai.ErrorResult("memory: old_path and new_path are the same"), nil
	}
	if under(from, to) {
		return ai.ErrorResult("memory: cannot move %s inside itself", from), nil
	}
	notes, err := n.list(accountID, userID)
	if err != nil {
		return ai.Result{}, err
	}
	byPath := make(map[string]note, len(notes))
	for _, nt := range notes {
		byPath[nt.Path] = nt
	}
	var moves [][2]string
	if _, ok := byPath[from]; ok {
		moves = append(moves, [2]string{from, to})
	} else {
		for _, nt := range notes {
			if under(from, nt.Path) {
				moves = append(moves, [2]string{nt.Path, to + strings.TrimPrefix(nt.Path, from)})
			}
		}
	}
	if len(moves) == 0 {
		return ai.ErrorResult("memory: %s does not exist", from), nil
	}
	for _, m := range moves {
		if _, exists := byPath[m[1]]; exists {
			return ai.ErrorResult("memory: %s already exists", m[1]), nil
		}
		if len(m[1]) > MaxPathBytes {
			return ai.ErrorResult("memory: %s is longer than %d bytes", m[1], MaxPathBytes), nil
		}
	}
	for _, m := range moves {
		nt := byPath[m[0]]
		if err := n.put(accountID, userID, m[1], nt.Content); err != nil {
			return ai.Result{}, err
		}
		if err := n.remove(accountID, userID, m[0]); err != nil {
			return ai.Result{}, err
		}
	}
	return ai.Result{Content: fmt.Sprintf("Successfully renamed %s to %s", from, to)}, nil
}

// NotesToolName is the name of the tool NotesTool returns. The Anthropic
// provider sends a tool with this name as the native memory_20250818
// tool type; other providers see the JSON schema below.
const NotesToolName = "memory"

const notesSchema = `{
  "type": "object",
  "properties": {
    "command": {"type": "string", "enum": ["view", "create", "str_replace", "insert", "delete", "rename"], "description": "The operation to run."},
    "path": {"type": "string", "description": "Absolute path under /memories (view, create, str_replace, insert, delete)."},
    "file_text": {"type": "string", "description": "Content to write (create)."},
    "old_str": {"type": "string", "description": "Text to replace; must occur exactly once (str_replace)."},
    "new_str": {"type": "string", "description": "Replacement text (str_replace)."},
    "insert_line": {"type": "integer", "minimum": 0, "description": "Insert after this line; 0 inserts at the top (insert)."},
    "insert_text": {"type": "string", "description": "Text to insert (insert)."},
    "old_path": {"type": "string", "description": "Current path (rename)."},
    "new_path": {"type": "string", "description": "New path (rename)."},
    "view_range": {"type": "array", "items": {"type": "integer"}, "minItems": 2, "maxItems": 2, "description": "Optional [start, end] 1-based line range; end -1 means the last line (view)."}
  },
  "required": ["command"]
}`

// NotesTool returns the "memory" tool over notes, acting as the caller
// placed in the context by ai.WithCaller.
func NotesTool(notes *Notes) ai.Tool {
	desc := "Persistent notes under /memories that survive between conversations. View the directory at the start of a task, read what is relevant, and record what should be remembered: preferences, decisions, and facts about the account. Commands: view, create, str_replace, insert, delete, rename."
	return ai.NewTool(NotesToolName, desc, json.RawMessage(notesSchema), false,
		func(ctx context.Context, input json.RawMessage) (ai.Result, error) {
			caller, ok := ai.CallerFromContext(ctx)
			if !ok || caller.AccountID == "" || caller.UserID == "" {
				return ai.ErrorResult("memory: no caller identity in context"), nil
			}
			return notes.Execute(ctx, caller.AccountID, caller.UserID, input)
		})
}
