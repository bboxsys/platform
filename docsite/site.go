// Package docsite serves a built documentation site (Astro Starlight, or
// any static site laid out the same way) from an fs.FS embedded in a
// product binary, behind the product's own sign-in, and reads the site's
// pages into sections the assistant can search and cite.
//
// A Site is loaded once from the built output (Load): every page under
// the tree is parsed, its main content split at the headings Starlight
// gave ids, and the text of each section kept with its heading path, the
// page's path and the anchor the browser scrolls to. The sections carry a
// keyword index (Search) that needs no model or credential, so the
// documentation is searchable the moment the process starts; package
// ai/docsearch adds semantic retrieval over the same sections and turns
// them into assistant tools.
//
// Handler serves the same tree to browsers. Pages are gated by a session
// cookie the SPA establishes with the user's bearer (POST {prefix}/_session),
// because a link in a chat reply or a bookmark carries no bearer; a page
// request without a session is redirected to the SPA's documentation gate,
// which signs the user in if needed and comes back.
//
// The Starlight side of the contract (base path, link rewriting, theme) is
// @bboxsys/ui/starlight; docs/DOCSITE.md describes both halves.
package docsite

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strings"
)

// Options configure Load.
type Options struct {
	// Project identifies the documentation set: the product's id
	// ("acme"), [a-z0-9_-]+. Required.
	Project string
	// Title names the documentation to readers and the assistant ("the
	// Acme documentation"); empty takes the site title from the root
	// page's <title>.
	Title string
	// Version is the product version the documentation describes, if the
	// host knows it; it is reported with the site, never required.
	Version string
}

var projectPattern = regexp.MustCompile(`^[a-z0-9_-]+$`)

// Site is a loaded documentation site: its pages in path order, every
// section of every page in reading order, and a keyword index over them.
type Site struct {
	Project  string
	Title    string
	Version  string
	Pages    []*Page
	Sections []*Section
	// Revision identifies the content: a hash over every section, so two
	// builds of the same sources produce the same revision.
	Revision string

	fs     fs.FS
	byID   map[string]*Section
	byPage map[string]*Page
	index  *keywordIndex
}

// Page is one HTML page of the site.
type Page struct {
	// Path is the page's path relative to the site root, with a trailing
	// slash for a directory index ("planning/retirement/") and "" for the
	// root page.
	Path        string
	Title       string
	Description string
	Sections    []*Section
	Hash        string
}

// Section is one heading of a page and the text under it, up to the next
// heading of any level.
type Section struct {
	// ID is the section's stable id: the page path plus "#anchor", or the
	// page path alone for the text above the first heading. The root
	// page's sections are "/" and "/#anchor".
	ID string
	// Page is the owning page's Path.
	Page string
	// Anchor is the heading's id, the fragment a browser scrolls to; "" for
	// the page's lead section.
	Anchor string
	// Level is the heading level, 1 for the page title.
	Level int
	// Title is the heading text.
	Title string
	// Headings is the heading path from the page title down to Title.
	Headings []string
	// Text is the section's content as plain text: paragraphs separated by
	// blank lines, code blocks fenced, list items and table rows one per
	// line.
	Text string
	// Hash identifies the section's content.
	Hash string
}

// Load reads a built site from fsys: every *.html file except 404.html,
// in path order. A page without a <main> element is skipped. The result
// is immutable; load again for a new build.
func Load(fsys fs.FS, o Options) (*Site, error) {
	if fsys == nil {
		return nil, errors.New("docsite: filesystem is nil")
	}
	if !projectPattern.MatchString(o.Project) {
		return nil, fmt.Errorf("docsite: invalid project id %q", o.Project)
	}
	s := &Site{
		Project: o.Project,
		Title:   o.Title,
		Version: o.Version,
		fs:      fsys,
		byID:    map[string]*Section{},
		byPage:  map[string]*Page{},
	}
	var files []string
	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if strings.HasPrefix(d.Name(), ".") && p != "." {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".html") || p == "404.html" {
			return nil
		}
		files = append(files, p)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("docsite: walk: %w", err)
	}
	sort.Strings(files)
	var siteTitle string
	for _, name := range files {
		data, err := fs.ReadFile(fsys, name)
		if err != nil {
			return nil, fmt.Errorf("docsite: read %s: %w", name, err)
		}
		page, docTitle, err := extractPage(pagePath(name), data)
		if err != nil {
			return nil, fmt.Errorf("docsite: %s: %w", name, err)
		}
		if page == nil {
			continue
		}
		if page.Path == "" && siteTitle == "" {
			// "Page | Site" names the site; the root page of a site whose
			// title is bare names it alone.
			siteTitle = docTitle
			if siteTitle == "" {
				siteTitle = page.Title
			}
		}
		s.Pages = append(s.Pages, page)
		s.byPage[page.Path] = page
		for _, sec := range page.Sections {
			s.Sections = append(s.Sections, sec)
			s.byID[sec.ID] = sec
		}
	}
	if len(s.Pages) == 0 {
		return nil, errors.New("docsite: no pages found")
	}
	if s.Title == "" {
		s.Title = siteTitle
	}
	if s.Title == "" {
		s.Title = o.Project
	}
	h := sha256.New()
	for _, p := range s.Pages {
		h.Write([]byte(p.Path))
		h.Write([]byte{0})
		h.Write([]byte(p.Hash))
		h.Write([]byte{0})
	}
	s.Revision = hex.EncodeToString(h.Sum(nil))[:16]
	s.index = newKeywordIndex(s.Sections)
	return s, nil
}

// pagePath turns a file name into a page path: "index.html" is "",
// "a/b/index.html" is "a/b/", and "a/b.html" is "a/b".
func pagePath(name string) string {
	name = path.Clean(name)
	if name == "index.html" {
		return ""
	}
	if strings.HasSuffix(name, "/index.html") {
		return strings.TrimSuffix(name, "index.html")
	}
	return strings.TrimSuffix(name, ".html")
}

// FS returns the built site the pages were read from, for Handler.
func (s *Site) FS() fs.FS { return s.fs }

// Section returns a section by id, or nil.
func (s *Site) Section(id string) *Section {
	if s == nil {
		return nil
	}
	return s.byID[normalizeID(id)]
}

// Page returns a page by path, or nil. A trailing "#anchor" is ignored
// and a missing trailing slash on a directory index is tolerated.
func (s *Site) Page(p string) *Page {
	if s == nil {
		return nil
	}
	p = strings.TrimPrefix(p, "/")
	if i := strings.IndexByte(p, '#'); i >= 0 {
		p = p[:i]
	}
	if page := s.byPage[p]; page != nil {
		return page
	}
	if p != "" && !strings.HasSuffix(p, "/") {
		return s.byPage[p+"/"]
	}
	return nil
}

// normalizeID accepts a section id with or without a leading slash, with
// "#_top" for the lead section, and the root page's sections as "/",
// "/#anchor" or "#anchor".
func normalizeID(id string) string {
	id = strings.TrimSpace(id)
	id = strings.TrimSuffix(id, "#_top")
	id = strings.TrimSuffix(id, "#")
	switch {
	case id == "" || id == "/":
		return "/"
	case strings.HasPrefix(id, "#"):
		return "/" + id
	case strings.HasPrefix(id, "/#"):
		return id
	}
	return strings.TrimPrefix(id, "/")
}

// URL returns the browser URL of a section under siteURL, the absolute
// URL the site is served at (no trailing slash).
func (sec *Section) URL(siteURL string) string {
	u := strings.TrimRight(siteURL, "/") + "/" + sec.Page
	if sec.Anchor != "" {
		u += "#" + sec.Anchor
	}
	return u
}

// HeadingPath renders the heading path as "Page title > Heading > Sub".
func (sec *Section) HeadingPath() string {
	return strings.Join(sec.Headings, " > ")
}

func contentHash(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}
