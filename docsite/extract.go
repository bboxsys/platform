package docsite

import (
	"bytes"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// extractPage parses one built page and splits its <main> content into
// sections at the headings that carry an id. It returns nil for a page
// without <main> (Starlight's 404 page, a redirect stub). docTitle is the
// site title taken from <title> ("Page | Site"), for the root page.
func extractPage(pagePath string, data []byte) (page *Page, docTitle string, err error) {
	doc, err := html.Parse(bytes.NewReader(data))
	if err != nil {
		return nil, "", err
	}
	var mainNode, titleNode, descNode *html.Node
	var find func(*html.Node)
	find = func(n *html.Node) {
		if n.Type == html.ElementNode {
			switch n.DataAtom {
			case atom.Main:
				if mainNode == nil {
					mainNode = n
				}
			case atom.Title:
				if titleNode == nil {
					titleNode = n
				}
			case atom.Meta:
				if attr(n, "name") == "description" && descNode == nil {
					descNode = n
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			find(c)
		}
	}
	find(doc)
	if mainNode == nil {
		return nil, "", nil
	}
	title, site := splitTitle(textOf(titleNode))
	page = &Page{Path: pagePath, Title: title}
	if descNode != nil {
		page.Description = strings.TrimSpace(attr(descNode, "content"))
	}

	ex := &extractor{page: page}
	ex.walk(mainNode, false)
	ex.flushBlock()
	ex.endSection()
	// The h1 is the page title as the reader sees it; <title> may add or
	// drop words.
	if len(page.Sections) > 0 && page.Sections[0].Level == 1 && page.Sections[0].Title != "" {
		page.Title = page.Sections[0].Title
	}
	for _, sec := range page.Sections {
		if len(sec.Headings) == 0 || sec.Headings[0] != page.Title {
			sec.Headings = append([]string{page.Title}, sec.Headings...)
		}
		sec.Hash = contentHash(sec.ID, sec.HeadingPath(), sec.Text)
	}
	var hashes []string
	for _, sec := range page.Sections {
		hashes = append(hashes, sec.Hash)
	}
	page.Hash = contentHash(append([]string{page.Path, page.Title, page.Description}, hashes...)...)
	return page, site, nil
}

// splitTitle separates "Page | Site" into its parts.
func splitTitle(t string) (page, site string) {
	t = strings.TrimSpace(t)
	if i := strings.LastIndex(t, " | "); i >= 0 {
		return strings.TrimSpace(t[:i]), strings.TrimSpace(t[i+3:])
	}
	return t, ""
}

// extractor walks a page's main content in document order, collecting
// blocks of text into the current section and opening a new section at
// every heading with an id.
type extractor struct {
	page *Page
	cur  *Section
	// stack holds the open heading titles by level, for the heading path.
	stack  []string
	blocks []string
	block  strings.Builder
	// listDepth is the nesting of the list being rendered.
	listDepth int
	ordinal   []int
}

var spaces = regexp.MustCompile(`[ \t\r\n\f]+`)

func (ex *extractor) walk(n *html.Node, pre bool) {
	switch n.Type {
	case html.TextNode:
		if pre {
			ex.block.WriteString(n.Data)
		} else {
			ex.block.WriteString(spaces.ReplaceAllString(n.Data, " "))
		}
		return
	case html.ElementNode:
	default:
		return
	}
	if skipElement(n) {
		return
	}
	switch n.DataAtom {
	case atom.H1, atom.H2, atom.H3, atom.H4:
		ex.flushBlock()
		id := attr(n, "id")
		if id == "" {
			// A heading without an id cannot be linked; keep it as text.
			ex.block.WriteString(textOf(n))
			ex.flushBlock()
			return
		}
		ex.startSection(headingLevel(n.DataAtom), id, strings.TrimSpace(spaces.ReplaceAllString(textOf(n), " ")))
		return
	case atom.Pre:
		ex.flushBlock()
		ex.block.WriteString("```")
		if lang := attr(n, "data-language"); lang != "" {
			ex.block.WriteString(lang)
		}
		ex.block.WriteString("\n")
		ex.block.WriteString(strings.TrimRight(preText(n), "\n"))
		ex.block.WriteString("\n```")
		ex.flushBlock()
		return
	case atom.Table:
		ex.flushBlock()
		ex.walkTable(n)
		return
	case atom.Ul, atom.Ol:
		ex.flushBlock()
		ex.listDepth++
		ex.ordinal = append(ex.ordinal, 0)
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if c.Type == html.ElementNode && c.DataAtom == atom.Li {
				ex.walkListItem(c, n.DataAtom == atom.Ol)
			}
		}
		ex.ordinal = ex.ordinal[:len(ex.ordinal)-1]
		ex.listDepth--
		return
	case atom.Img:
		if alt := strings.TrimSpace(attr(n, "alt")); alt != "" {
			ex.block.WriteString("[image: " + alt + "]")
		}
		return
	case atom.Br:
		ex.block.WriteString("\n")
		return
	case atom.Code:
		if !pre {
			ex.block.WriteString("`")
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				ex.walk(c, pre)
			}
			ex.block.WriteString("`")
			return
		}
	}
	block := isBlock(n.DataAtom)
	if block {
		ex.flushBlock()
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		ex.walk(c, pre)
	}
	if block {
		ex.flushBlock()
	}
}

// walkListItem renders one list item as a line with its marker, then any
// nested lists under it.
func (ex *extractor) walkListItem(li *html.Node, ordered bool) {
	marker := "- "
	if ordered {
		ex.ordinal[len(ex.ordinal)-1]++
		marker = strconv.Itoa(ex.ordinal[len(ex.ordinal)-1]) + ". "
	}
	indent := strings.Repeat("  ", ex.listDepth-1)
	var inline strings.Builder
	var nested []*html.Node
	for c := li.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.ElementNode && (c.DataAtom == atom.Ul || c.DataAtom == atom.Ol) {
			nested = append(nested, c)
			continue
		}
		sub := &extractor{page: ex.page, cur: &Section{}}
		sub.walk(c, false)
		sub.flushBlock()
		inline.WriteString(" ")
		inline.WriteString(strings.Join(sub.blocks, " "))
	}
	text := strings.TrimSpace(spaces.ReplaceAllString(inline.String(), " "))
	ex.block.WriteString(indent + marker + text)
	ex.flushLine()
	for _, l := range nested {
		ex.listDepth++
		ex.ordinal = append(ex.ordinal, 0)
		for c := l.FirstChild; c != nil; c = c.NextSibling {
			if c.Type == html.ElementNode && c.DataAtom == atom.Li {
				ex.walkListItem(c, l.DataAtom == atom.Ol)
			}
		}
		ex.ordinal = ex.ordinal[:len(ex.ordinal)-1]
		ex.listDepth--
	}
}

// walkTable renders rows as "cell | cell" lines; the header row is
// followed by a rule so the model can tell it apart.
func (ex *extractor) walkTable(t *html.Node) {
	var rows [][]string
	var header bool
	var visit func(*html.Node, bool)
	visit = func(n *html.Node, inHead bool) {
		if n.Type == html.ElementNode {
			switch n.DataAtom {
			case atom.Thead:
				inHead = true
			case atom.Tr:
				var cells []string
				for c := n.FirstChild; c != nil; c = c.NextSibling {
					if c.Type == html.ElementNode && (c.DataAtom == atom.Td || c.DataAtom == atom.Th) {
						cells = append(cells, strings.TrimSpace(spaces.ReplaceAllString(textOf(c), " ")))
					}
				}
				if inHead && len(rows) == 0 {
					header = true
				}
				rows = append(rows, cells)
				return
			case atom.Caption:
				ex.block.WriteString(strings.TrimSpace(spaces.ReplaceAllString(textOf(n), " ")))
				ex.flushLine()
				return
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			visit(c, inHead)
		}
	}
	visit(t, false)
	for i, cells := range rows {
		ex.block.WriteString(strings.Join(cells, " | "))
		ex.flushLine()
		if i == 0 && header {
			ex.block.WriteString(strings.Repeat("-", 3))
			ex.flushLine()
		}
	}
	ex.flushBlock()
}

// flushLine ends a line inside the current block (list items, table rows).
func (ex *extractor) flushLine() {
	s := strings.TrimRight(ex.block.String(), " ")
	ex.block.Reset()
	ex.block.WriteString(s)
	ex.block.WriteString("\n")
}

// flushBlock closes the current block of text.
func (ex *extractor) flushBlock() {
	text := strings.TrimSpace(ex.block.String())
	ex.block.Reset()
	if text == "" {
		return
	}
	if !strings.HasPrefix(text, "```") {
		// Inline elements leave runs of spaces between them; a code block
		// keeps its indentation.
		text = collapseSpaces(text)
	}
	if ex.cur == nil {
		ex.startSection(1, "", ex.page.Title)
	}
	ex.blocks = append(ex.blocks, text)
}

// startSection closes the current section and opens one for a heading.
func (ex *extractor) startSection(level int, id, title string) {
	ex.endSection()
	if level < 1 {
		level = 1
	}
	anchor := id
	if anchor == "_top" {
		anchor = ""
	}
	if len(ex.stack) >= level {
		ex.stack = ex.stack[:level-1]
	}
	for len(ex.stack) < level-1 {
		ex.stack = append(ex.stack, "")
	}
	ex.stack = append(ex.stack, title)
	var headings []string
	for _, h := range ex.stack {
		if h != "" {
			headings = append(headings, h)
		}
	}
	sec := &Section{Page: ex.page.Path, Anchor: anchor, Level: level, Title: title, Headings: headings}
	sec.ID = ex.page.Path
	if sec.ID == "" {
		sec.ID = "/"
	}
	if anchor != "" {
		sec.ID += "#" + anchor
	}
	ex.cur = sec
}

// endSection stores the current section with its text. A lead section
// (no anchor) with no text is dropped; a heading with no text is kept so
// the outline stays complete.
func (ex *extractor) endSection() {
	if ex.cur == nil {
		return
	}
	ex.cur.Text = strings.Join(ex.blocks, "\n\n")
	ex.blocks = nil
	if ex.cur.Anchor != "" || ex.cur.Text != "" {
		// Two headings with the same id would collide; keep the first.
		dup := false
		for _, s := range ex.page.Sections {
			if s.ID == ex.cur.ID {
				dup = true
				break
			}
		}
		if !dup {
			ex.page.Sections = append(ex.page.Sections, ex.cur)
		}
	}
	ex.cur = nil
}

// skipElement reports elements that are chrome rather than content:
// scripts, navigation, the page footer, buttons, icons, and anything the
// site marked with data-pagefind-ignore (Starlight's "Section titled"
// labels, for example) or as an anchor link.
func skipElement(n *html.Node) bool {
	switch n.DataAtom {
	case atom.Script, atom.Style, atom.Nav, atom.Footer, atom.Template, atom.Svg, atom.Button, atom.Select, atom.Noscript, atom.Iframe:
		return true
	}
	for _, a := range n.Attr {
		switch a.Key {
		case "data-pagefind-ignore":
			return true
		case "aria-hidden":
			if a.Val == "true" {
				return true
			}
		case "class":
			for _, c := range strings.Fields(a.Val) {
				switch c {
				case "sl-anchor-link", "sr-only", "right-sidebar", "pagination-links", "copy":
					return true
				}
			}
		}
	}
	return false
}

func isBlock(a atom.Atom) bool {
	switch a {
	case atom.P, atom.Div, atom.Section, atom.Article, atom.Aside, atom.Blockquote, atom.Figure, atom.Figcaption,
		atom.Details, atom.Summary, atom.H5, atom.H6, atom.Dl, atom.Dt, atom.Dd, atom.Hr, atom.Header, atom.Main, atom.Li:
		return true
	}
	return false
}

func headingLevel(a atom.Atom) int {
	switch a {
	case atom.H1:
		return 1
	case atom.H2:
		return 2
	case atom.H3:
		return 3
	default:
		return 4
	}
}

// preText renders a code block. Expressive Code (Starlight's renderer)
// puts each line in a div.ec-line, so those start a new line; plain
// <pre><code> keeps its own newlines.
func preText(n *html.Node) string {
	var b strings.Builder
	first := true
	var visit func(*html.Node)
	visit = func(n *html.Node) {
		switch n.Type {
		case html.TextNode:
			b.WriteString(n.Data)
			return
		case html.ElementNode:
			if skipElement(n) {
				return
			}
			if hasClass(n, "ec-line") {
				if !first {
					b.WriteString("\n")
				}
				first = false
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			visit(c)
		}
	}
	visit(n)
	return b.String()
}

// textOf returns the text content of a node, skipping chrome elements.
func textOf(n *html.Node) string {
	if n == nil {
		return ""
	}
	var b strings.Builder
	var visit func(*html.Node)
	visit = func(n *html.Node) {
		switch n.Type {
		case html.TextNode:
			b.WriteString(n.Data)
			return
		case html.ElementNode:
			if skipElement(n) {
				return
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			visit(c)
		}
	}
	visit(n)
	return b.String()
}

var spaceRuns = regexp.MustCompile(`(\S)[ \t]{2,}`)

// collapseSpaces folds runs of spaces inside each line of a block.
func collapseSpaces(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(spaceRuns.ReplaceAllString(l, "$1 "), " ")
	}
	return strings.Join(lines, "\n")
}

func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

func hasClass(n *html.Node, class string) bool {
	for _, c := range strings.Fields(attr(n, "class")) {
		if c == class {
			return true
		}
	}
	return false
}

// trimRunes shortens s to at most n runes, cutting at a space when one is
// near the end, and appends an ellipsis when it cut.
func trimRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	cut := n
	for i := n; i > n*3/4; i-- {
		if unicode.IsSpace(r[i]) {
			cut = i
			break
		}
	}
	return strings.TrimRight(string(r[:cut]), " \n") + " ..."
}
