package docsite

import (
	"strings"
	"testing"
	"testing/fstest"
)

// fixture is a cut-down Starlight page: the chrome around <main>, the
// title block, anchored headings with their "Section titled" labels, an
// aside, a table, lists, and an Expressive Code block.
const fixturePage = `<!DOCTYPE html><html><head><title>The Rule of 55 | Acme</title>
<meta name="description" content="The exception that lets someone draw a 401(k) at 55."></head>
<body><header><nav>Skip to content</nav><a href="/docs/">Acme</a></header>
<nav id="starlight__sidebar"><a href="/docs/planning/retirement/">Retirement</a></nav>
<main data-pagefind-body>
 <div class="content-panel"><h1 id="_top">The Rule of 55</h1></div>
 <div class="content-panel"><div class="sl-markdown-content">
  <p>The Internal Revenue Code taxes most   withdrawals before age 59 and a half.
   One exception is the reason a retirement at <strong>55</strong> can work.</p>
  <aside class="starlight-aside starlight-aside--caution"><p class="starlight-aside__title"><svg aria-hidden="true"><path d="M0"/></svg>Not advice</p>
   <div class="starlight-aside__content"><p>This is research, not tax advice.</p></div></aside>
  <div class="sl-heading-wrapper level-h2"><h2 id="the-rule">The rule</h2><a class="sl-anchor-link" href="#the-rule"><span class="sr-only" data-pagefind-ignore>Section titled The rule</span></a></div>
  <div class="sl-heading-wrapper level-h3"><h3 id="what-the-statute-says">What the statute says</h3><a class="sl-anchor-link" href="#what-the-statute-says">#</a></div>
  <p>Section 72(t)(1) imposes a 10 percent additional tax. Set <code>ai.allow_writes</code> to change it.</p>
  <table><thead><tr><th>Element</th><th>Meaning</th></tr></thead>
   <tbody><tr><td>Qualified plan</td><td>A 401(k) or 403(b)</td></tr><tr><td>Separation</td><td>Leaving the employer</td></tr></tbody></table>
  <ul><li>First point</li><li>Second point<ul><li>Nested detail</li></ul></li></ul>
  <ol><li>Step one</li><li>Step two</li></ol>
  <div class="expressive-code"><figure class="frame"><figcaption class="header"><span class="title">config.toml</span></figcaption>
   <pre data-language="toml"><code><div class="ec-line"><div class="code">[ai]</div></div><div class="ec-line"><div class="code">  allow_writes = true</div></div></code></pre>
   <div class="copy"><button title="Copy">Copy</button></div></figure></div>
  <div class="sl-heading-wrapper level-h2"><h2 id="sources">Sources</h2><a class="sl-anchor-link" href="#sources">#</a></div>
  <p>IRS Topic 558.</p>
 </div></div>
 <footer><a href="/docs/planning/retirement/">Previous</a><p>Last updated</p></footer>
</main>
<aside class="right-sidebar-container"><nav>On this page</nav></aside>
</body></html>`

const fixtureIndex = `<!DOCTYPE html><html><head><title>Acme</title></head><body>
<main><h1 id="_top">Acme</h1><div class="sl-markdown-content"><p>Research notes.</p>
<h2 id="planning">Planning</h2><p>Retirement and the Rule of 55.</p></div></main></body></html>`

func fixtureFS() fstest.MapFS {
	return fstest.MapFS{
		"index.html":                     {Data: []byte(fixtureIndex)},
		"404.html":                       {Data: []byte("<html><body><main><h1>Not found</h1></main></body></html>")},
		"planning/rule-of-55/index.html": {Data: []byte(fixturePage)},
		"_astro/app.abc123.js":           {Data: []byte("console.log(1)")},
		"_astro/app.abc123.js.gz":        {Data: []byte("gz-bytes")},
		"pagefind/pagefind-entry.json":   {Data: []byte("{}")},
	}
}

func TestLoadExtractsSections(t *testing.T) {
	s, err := Load(fixtureFS(), Options{Project: "acme"})
	if err != nil {
		t.Fatal(err)
	}
	if s.Title != "Acme" {
		t.Errorf("title %q", s.Title)
	}
	if len(s.Pages) != 2 {
		t.Fatalf("pages %d", len(s.Pages))
	}
	if s.Revision == "" {
		t.Error("revision empty")
	}
	p := s.Page("planning/rule-of-55")
	if p == nil {
		t.Fatal("page not found without trailing slash")
	}
	if p.Title != "The Rule of 55" || !strings.HasPrefix(p.Description, "The exception") {
		t.Errorf("page %+v", p)
	}
	ids := make([]string, 0, len(p.Sections))
	for _, sec := range p.Sections {
		ids = append(ids, sec.ID)
	}
	want := []string{"planning/rule-of-55/", "planning/rule-of-55/#the-rule", "planning/rule-of-55/#what-the-statute-says", "planning/rule-of-55/#sources"}
	if strings.Join(ids, ",") != strings.Join(want, ",") {
		t.Errorf("sections %v", ids)
	}

	lead := p.Sections[0]
	if strings.Contains(lead.Text, "  ") || !strings.Contains(lead.Text, "retirement at 55 can work") {
		t.Errorf("lead text %q", lead.Text)
	}
	if !strings.Contains(lead.Text, "Not advice\n\nThis is research") {
		t.Errorf("aside not rendered: %q", lead.Text)
	}
	if strings.Contains(lead.Text, "Section titled") || strings.Contains(lead.Text, "Last updated") || strings.Contains(lead.Text, "On this page") {
		t.Errorf("chrome leaked: %q", lead.Text)
	}

	rule := s.Section("planning/rule-of-55/#the-rule")
	if rule == nil || rule.Text != "" || rule.HeadingPath() != "The Rule of 55 > The rule" {
		t.Errorf("rule section %+v", rule)
	}
	statute := s.Section("/planning/rule-of-55/#what-the-statute-says")
	if statute == nil {
		t.Fatal("statute section missing")
	}
	if statute.HeadingPath() != "The Rule of 55 > The rule > What the statute says" {
		t.Errorf("heading path %q", statute.HeadingPath())
	}
	for _, want := range []string{
		"`ai.allow_writes`",
		"Element | Meaning\n---\nQualified plan | A 401(k) or 403(b)\nSeparation | Leaving the employer",
		"- First point\n- Second point\n  - Nested detail",
		"1. Step one\n2. Step two",
		"config.toml",
		"```toml\n[ai]\n  allow_writes = true\n```",
	} {
		if !strings.Contains(statute.Text, want) {
			t.Errorf("statute text missing %q:\n%s", want, statute.Text)
		}
	}
	if strings.Contains(statute.Text, "Copy") {
		t.Errorf("copy button leaked: %q", statute.Text)
	}
	if got := statute.URL("https://host:9680/docs/"); got != "https://host:9680/docs/planning/rule-of-55/#what-the-statute-says" {
		t.Errorf("url %q", got)
	}
	if got := s.Section("planning/rule-of-55/#_top"); got != lead {
		t.Error("#_top should resolve to the lead section")
	}
	root := s.Page("/")
	if root == nil || root.Sections[0].ID != "/" || root.Sections[1].ID != "/#planning" {
		t.Errorf("root sections %+v", root)
	}
	if s.Section("#planning") == nil || s.Section("/#planning") == nil || s.Section("") != root.Sections[0] {
		t.Error("root section lookups")
	}
	if got := root.Sections[1].URL("https://h/docs"); got != "https://h/docs/#planning" {
		t.Errorf("root url %q", got)
	}
}

func TestLoadRejectsBadInput(t *testing.T) {
	if _, err := Load(fixtureFS(), Options{Project: "Bad Id"}); err == nil {
		t.Error("expected invalid project error")
	}
	if _, err := Load(fstest.MapFS{"404.html": {Data: []byte("<html></html>")}}, Options{Project: "x"}); err == nil {
		t.Error("expected no pages error")
	}
	if _, err := Load(nil, Options{Project: "x"}); err == nil {
		t.Error("expected nil fs error")
	}
}

func TestSearch(t *testing.T) {
	s, err := Load(fixtureFS(), Options{Project: "acme"})
	if err != nil {
		t.Fatal(err)
	}
	hits := s.Search("what does the statute say", 3)
	if len(hits) == 0 || hits[0].Section.ID != "planning/rule-of-55/#what-the-statute-says" {
		t.Fatalf("hits %+v", hits)
	}
	if !strings.Contains(hits[0].Snippet, "statute") && !strings.Contains(hits[0].Snippet, "Section 72") {
		t.Errorf("snippet %q", hits[0].Snippet)
	}
	// An exact configuration key finds its section.
	hits = s.Search("ai.allow_writes", 1)
	if len(hits) != 1 || hits[0].Section.ID != "planning/rule-of-55/#what-the-statute-says" {
		t.Errorf("identifier search %+v", hits)
	}
	// Plurals fold.
	if hits := s.Search("sources", 1); len(hits) != 1 || hits[0].Section.ID != "planning/rule-of-55/#sources" {
		t.Errorf("stemmed search %+v", hits)
	}
	if hits := s.Search("zzzz qqqq", 3); len(hits) != 0 {
		t.Errorf("expected no hits, got %+v", hits)
	}
	if hits := s.Search("", 3); hits != nil {
		t.Errorf("empty query should be nil")
	}
}

func TestTokenize(t *testing.T) {
	got := strings.Join(tokenize("Set ai.allow_writes, then GET /api/v1/queue; portfolios!"), " ")
	want := "set ai.allow_writes ai allow write then get api/v1/queue api v1 queue portfolio"
	if got != want {
		t.Errorf("tokenize:\n got %q\nwant %q", got, want)
	}
}
