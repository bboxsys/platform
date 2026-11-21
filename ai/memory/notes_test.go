package memory

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/bboxsys/platform/ai"
	"github.com/bboxsys/store"
)

// quietLogger keeps badger's startup chatter out of the test output.
type quietLogger struct{}

func (quietLogger) Debug(string) {}
func (quietLogger) Info(string)  {}
func (quietLogger) Warn(string)  {}
func (quietLogger) Error(string) {}

func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.NewStore(&store.Options{
		Location:    t.TempDir(),
		Application: "unit_testing",
		Component:   "ai_memory",
		Logger:      quietLogger{},
	})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

// run executes one command and fails the test on a Go error.
func run(t *testing.T, n *Notes, account, user, input string) ai.Result {
	t.Helper()
	res, err := n.Execute(context.Background(), account, user, json.RawMessage(input))
	if err != nil {
		t.Fatalf("Execute(%s): %v", input, err)
	}
	return res
}

func mustOK(t *testing.T, res ai.Result, want string) {
	t.Helper()
	if res.IsError {
		t.Fatalf("unexpected error result: %s", res.Content)
	}
	if want != "" && !strings.Contains(res.Content, want) {
		t.Fatalf("result %q does not contain %q", res.Content, want)
	}
}

func mustErr(t *testing.T, res ai.Result, want string) {
	t.Helper()
	if !res.IsError {
		t.Fatalf("expected error result, got %q", res.Content)
	}
	if want != "" && !strings.Contains(res.Content, want) {
		t.Fatalf("error %q does not contain %q", res.Content, want)
	}
}

func TestNotesCommands(t *testing.T) {
	ctx := context.Background()
	n := NewNotes(newTestStore(t))
	const acct, user = "acct1", "UABC"

	// An empty root views as empty, not as an error.
	mustOK(t, run(t, n, acct, user, `{"command":"view","path":"/memories"}`), "(empty)")

	// create, then view the file with line numbers.
	mustOK(t, run(t, n, acct, user, `{"command":"create","path":"/memories/prefs.md","file_text":"# Prefs\nlikes tea\n"}`), "File created successfully at: /memories/prefs.md")
	res := run(t, n, acct, user, `{"command":"view","path":"/memories/prefs.md"}`)
	mustOK(t, res, "")
	if !strings.Contains(res.Content, "     1\t# Prefs\n     2\tlikes tea\n") {
		t.Errorf("numbered view: %q", res.Content)
	}

	// view_range.
	res = run(t, n, acct, user, `{"command":"view","path":"/memories/prefs.md","view_range":[2,-1]}`)
	mustOK(t, res, "     2\tlikes tea")
	if strings.Contains(res.Content, "# Prefs") {
		t.Errorf("view_range returned line 1: %q", res.Content)
	}
	mustErr(t, run(t, n, acct, user, `{"command":"view","path":"/memories/prefs.md","view_range":[0,1]}`), "view_range")
	mustErr(t, run(t, n, acct, user, `{"command":"view","path":"/memories/prefs.md","view_range":[1,9]}`), "view_range")

	// The directory listing shows files and subdirectories.
	mustOK(t, run(t, n, acct, user, `{"command":"create","path":"/memories/projects/alpha.md","file_text":"alpha"}`), "")
	mustOK(t, run(t, n, acct, user, `{"command":"create","path":"/memories/projects/deep/nested/x.md","file_text":"x"}`), "")
	res = run(t, n, acct, user, `{"command":"view","path":"/memories"}`)
	mustOK(t, res, "")
	for _, want := range []string{"/memories/prefs.md\n", "/memories/projects/\n", "/memories/projects/alpha.md\n", "/memories/projects/deep/\n"} {
		if !strings.Contains(res.Content, want) {
			t.Errorf("directory view missing %q:\n%s", want, res.Content)
		}
	}
	if strings.Contains(res.Content, "x.md") {
		t.Errorf("directory view went deeper than two levels:\n%s", res.Content)
	}
	mustOK(t, run(t, n, acct, user, `{"command":"view","path":"/memories/projects/"}`), "/memories/projects/alpha.md")
	mustErr(t, run(t, n, acct, user, `{"command":"view","path":"/memories/nope"}`), "does not exist")

	// A file cannot be created where a directory is, nor under a file.
	mustErr(t, run(t, n, acct, user, `{"command":"create","path":"/memories/projects","file_text":"x"}`), "is a directory")
	mustErr(t, run(t, n, acct, user, `{"command":"create","path":"/memories/prefs.md/sub.md","file_text":"x"}`), "is a file")
	mustErr(t, run(t, n, acct, user, `{"command":"create","path":"/memories","file_text":"x"}`), "is a directory")

	// str_replace: exactly one match.
	mustOK(t, run(t, n, acct, user, `{"command":"str_replace","path":"/memories/prefs.md","old_str":"tea","new_str":"coffee"}`), "The memory file has been edited.")
	if got, _ := n.Read(ctx, acct, user, "/memories/prefs.md"); got != "# Prefs\nlikes coffee\n" {
		t.Errorf("after str_replace: %q", got)
	}
	mustErr(t, run(t, n, acct, user, `{"command":"str_replace","path":"/memories/prefs.md","old_str":"tea","new_str":"x"}`), "did not appear")
	mustOK(t, run(t, n, acct, user, `{"command":"create","path":"/memories/dup.md","file_text":"a b a\n"}`), "")
	mustErr(t, run(t, n, acct, user, `{"command":"str_replace","path":"/memories/dup.md","old_str":"a","new_str":"x"}`), "appears 2 times")
	if got, _ := n.Read(ctx, acct, user, "/memories/dup.md"); got != "a b a\n" {
		t.Errorf("multiple-match str_replace changed the file: %q", got)
	}
	mustErr(t, run(t, n, acct, user, `{"command":"str_replace","path":"/memories/missing.md","old_str":"a","new_str":"x"}`), "does not exist")
	mustErr(t, run(t, n, acct, user, `{"command":"str_replace","path":"/memories/dup.md","old_str":"","new_str":"x"}`), "old_str is required")

	// insert: 0 is the top, len(lines) is the end, beyond is an error.
	mustOK(t, run(t, n, acct, user, `{"command":"insert","path":"/memories/prefs.md","insert_line":0,"insert_text":"top"}`), "edited")
	mustOK(t, run(t, n, acct, user, `{"command":"insert","path":"/memories/prefs.md","insert_line":3,"insert_text":"end\n"}`), "edited")
	if got, _ := n.Read(ctx, acct, user, "/memories/prefs.md"); got != "top\n# Prefs\nlikes coffee\nend\n" {
		t.Errorf("after inserts: %q", got)
	}
	mustErr(t, run(t, n, acct, user, `{"command":"insert","path":"/memories/prefs.md","insert_line":9,"insert_text":"x"}`), "outside 0..4")
	mustErr(t, run(t, n, acct, user, `{"command":"insert","path":"/memories/prefs.md","insert_text":"x"}`), "insert_line is required")

	// rename a file, then a directory.
	mustOK(t, run(t, n, acct, user, `{"command":"rename","old_path":"/memories/dup.md","new_path":"/memories/moved.md"}`), "Successfully renamed /memories/dup.md to /memories/moved.md")
	if _, err := n.Read(ctx, acct, user, "/memories/dup.md"); !errors.Is(err, ErrNotFound) {
		t.Errorf("old path still readable: %v", err)
	}
	if got, _ := n.Read(ctx, acct, user, "/memories/moved.md"); got != "a b a\n" {
		t.Errorf("moved content: %q", got)
	}
	mustErr(t, run(t, n, acct, user, `{"command":"rename","old_path":"/memories/moved.md","new_path":"/memories/prefs.md"}`), "already exists")
	mustOK(t, run(t, n, acct, user, `{"command":"rename","old_path":"/memories/projects","new_path":"/memories/work"}`), "Successfully renamed")
	paths, err := n.List(ctx, acct, user)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(paths, ",") != "/memories/moved.md,/memories/prefs.md,/memories/work/alpha.md,/memories/work/deep/nested/x.md" {
		t.Errorf("after directory rename: %v", paths)
	}
	mustErr(t, run(t, n, acct, user, `{"command":"rename","old_path":"/memories/work","new_path":"/memories/work/inner"}`), "inside itself")
	mustErr(t, run(t, n, acct, user, `{"command":"rename","old_path":"/memories/gone","new_path":"/memories/x"}`), "does not exist")

	// delete a file, a directory subtree, and a missing path.
	mustOK(t, run(t, n, acct, user, `{"command":"delete","path":"/memories/moved.md"}`), "Successfully deleted /memories/moved.md")
	mustOK(t, run(t, n, acct, user, `{"command":"delete","path":"/memories/work"}`), "Successfully deleted /memories/work")
	paths, _ = n.List(ctx, acct, user)
	if len(paths) != 1 || paths[0] != "/memories/prefs.md" {
		t.Errorf("after deletes: %v", paths)
	}
	mustErr(t, run(t, n, acct, user, `{"command":"delete","path":"/memories/work"}`), "does not exist")

	// Unknown and missing commands, bad JSON.
	mustErr(t, run(t, n, acct, user, `{"command":"zap","path":"/memories/prefs.md"}`), "unknown command")
	mustErr(t, run(t, n, acct, user, `{"path":"/memories/prefs.md"}`), "command is required")
	mustErr(t, run(t, n, acct, user, `{nope`), "invalid input")

	// Size limit.
	big := strings.Repeat("x", MaxFileBytes+1)
	in, _ := json.Marshal(map[string]string{"command": "create", "path": "/memories/big.md", "file_text": big})
	mustErr(t, run(t, n, acct, user, string(in)), "limit")
}

func TestNotesPathRejection(t *testing.T) {
	n := NewNotes(newTestStore(t))
	const acct, user = "acct1", "UABC"
	bad := []string{
		"",
		"/etc/passwd",
		"memories/x.md",
		"/memories/../etc/passwd",
		"/memories/a/../../x",
		"/memories/..",
		"/memoriesx/a.md",
		"/memories/a\x00b",
		"/memories/" + strings.Repeat("a", MaxPathBytes),
	}
	for _, p := range bad {
		in, _ := json.Marshal(map[string]string{"command": "create", "path": p, "file_text": "x"})
		res := run(t, n, acct, user, string(in))
		if !res.IsError {
			t.Errorf("path %q accepted", p)
		}
		if _, err := NormalizePath(p); err == nil {
			t.Errorf("NormalizePath(%q) accepted", p)
		}
	}
	// Clean paths are normalized, and ".." that stays inside the root is
	// still refused (the model should never need it).
	if got, err := NormalizePath("/memories//a/./b.md"); err != nil || got != "/memories/a/b.md" {
		t.Errorf("NormalizePath clean: %q %v", got, err)
	}
	if _, err := NormalizePath("/memories/a/../b.md"); err == nil {
		t.Errorf("NormalizePath accepted an inner ..")
	}
	paths, err := n.List(context.Background(), acct, user)
	if err != nil || len(paths) != 0 {
		t.Errorf("rejected paths wrote notes: %v %v", paths, err)
	}
}

func TestNotesIsolation(t *testing.T) {
	ctx := context.Background()
	n := NewNotes(newTestStore(t))
	mustOK(t, run(t, n, "acctA", "U1", `{"command":"create","path":"/memories/a.md","file_text":"A1"}`), "")
	mustOK(t, run(t, n, "acctA", "U2", `{"command":"create","path":"/memories/a.md","file_text":"A2"}`), "")
	mustOK(t, run(t, n, "acctB", "U1", `{"command":"create","path":"/memories/b.md","file_text":"B1"}`), "")

	for _, c := range []struct{ acct, user, want string }{
		{"acctA", "U1", "/memories/a.md"},
		{"acctA", "U2", "/memories/a.md"},
		{"acctB", "U1", "/memories/b.md"},
	} {
		paths, err := n.List(ctx, c.acct, c.user)
		if err != nil || len(paths) != 1 || paths[0] != c.want {
			t.Errorf("%s/%s: %v %v", c.acct, c.user, paths, err)
		}
	}
	if got, _ := n.Read(ctx, "acctA", "U2", "/memories/a.md"); got != "A2" {
		t.Errorf("user U2 read %q", got)
	}
	if _, err := n.Read(ctx, "acctB", "U2", "/memories/a.md"); !errors.Is(err, ErrNotFound) {
		t.Errorf("cross-account read: %v", err)
	}
	// A user id that is a prefix of another does not see the other's notes.
	mustOK(t, run(t, n, "acctA", "U", `{"command":"create","path":"/memories/u.md","file_text":"U"}`), "")
	paths, _ := n.List(ctx, "acctA", "U")
	if len(paths) != 1 || paths[0] != "/memories/u.md" {
		t.Errorf("prefix user saw %v", paths)
	}
	// Deleting one user's root leaves the others alone.
	mustOK(t, run(t, n, "acctA", "U1", `{"command":"delete","path":"/memories"}`), "")
	if p, _ := n.List(ctx, "acctA", "U1"); len(p) != 0 {
		t.Errorf("U1 notes remain: %v", p)
	}
	if p, _ := n.List(ctx, "acctA", "U2"); len(p) != 1 {
		t.Errorf("U2 notes affected: %v", p)
	}
	// Invalid identities are refused.
	if _, err := n.Execute(ctx, "", "U1", json.RawMessage(`{"command":"view","path":"/memories"}`)); err == nil {
		t.Errorf("empty account accepted")
	}
	if _, err := n.Execute(ctx, "acctA", "a:b", json.RawMessage(`{"command":"view","path":"/memories"}`)); err == nil {
		t.Errorf("user id with colon accepted")
	}
}

func TestNotesTool(t *testing.T) {
	ctx := context.Background()
	n := NewNotes(newTestStore(t))
	tool := NotesTool(n)
	if tool.Name() != "memory" || tool.ReadOnly() {
		t.Fatalf("tool shape: %s readonly=%v", tool.Name(), tool.ReadOnly())
	}
	var schema map[string]any
	if err := json.Unmarshal(tool.InputSchema(), &schema); err != nil {
		t.Fatalf("schema: %v", err)
	}
	props := schema["properties"].(map[string]any)
	for _, k := range []string{"command", "path", "file_text", "old_str", "new_str", "insert_line", "insert_text", "old_path", "new_path"} {
		if _, ok := props[k]; !ok {
			t.Errorf("schema missing %s", k)
		}
	}

	res, err := tool.Execute(ctx, json.RawMessage(`{"command":"view","path":"/memories"}`))
	if err != nil || !res.IsError || !strings.Contains(res.Content, "caller") {
		t.Fatalf("missing caller: %+v %v", res, err)
	}
	res, err = tool.Execute(ai.WithCaller(ctx, ai.Caller{AccountID: "acct1"}), json.RawMessage(`{"command":"view","path":"/memories"}`))
	if err != nil || !res.IsError {
		t.Fatalf("caller without user: %+v %v", res, err)
	}

	cctx := ai.WithCaller(ctx, ai.Caller{AccountID: "acct1", UserID: "U1"})
	res, err = tool.Execute(cctx, json.RawMessage(`{"command":"create","path":"/memories/t.md","file_text":"hello"}`))
	if err != nil || res.IsError {
		t.Fatalf("create through tool: %+v %v", res, err)
	}
	if got, err := n.Read(ctx, "acct1", "U1", "/memories/t.md"); err != nil || got != "hello" {
		t.Errorf("tool wrote %q %v", got, err)
	}
}
