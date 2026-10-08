package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"strings"
	"testing"
)

func TestHandleWikiAPIHTML(t *testing.T) {
	tmp := withTempWiki(t)
	if err := os.WriteFile(path.Join(tmp, "wiki", "Server", "Rack Plan.htm"), []byte("<p>rack</p>"), 0o644); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/wiki?path=Server/Rack%20Plan.htm", nil)
	rec := httptest.NewRecorder()
	HandleWikiAPI(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var got APIWikiResponse
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	want := APIWikiResponse{
		Mode:    "html",
		Title:   "Rack Plan",
		RelPath: "Server/Rack Plan.htm",
		FileURL: "/api/download/Server/Rack%20Plan.htm",
	}
	if fmt.Sprintf("%+v", got) != fmt.Sprintf("%+v", want) {
		t.Errorf("response = %+v, want %+v", got, want)
	}
}

func TestListingsShowHTMLPages(t *testing.T) {
	tmp := withTempWiki(t)
	for name, body := range map[string]string{"guide.md": "# Guide", "Export.HTML": "<p>x</p>", "style.css": "p{}"} {
		if err := os.WriteFile(path.Join(tmp, "wiki", "Server", name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	nav, err := GenerateSidebarContents()
	if err != nil {
		t.Fatal(err)
	}
	items := nav[NavigationElement{Title: "Server", Link: "/wiki/Server"}]
	want := []NavigationElement{
		{Title: "Export", Link: "/wiki/Server/Export.HTML", Kind: "html"},
		{Title: "Guide", Link: "/wiki/Server/guide"},
	}
	if fmt.Sprint(items) != fmt.Sprint(want) {
		t.Errorf("sidebar = %+v, want %+v", items, want)
	}

	_, articles, _, files, err := loadDirectoryByRelPath("Server")
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(articles) != fmt.Sprint(want) {
		t.Errorf("articles = %+v, want %+v", articles, want)
	}
	if len(files) != 1 || files[0].Title != "style.css" {
		t.Errorf("files = %+v, want style.css only", files)
	}
}

func TestExpandWikiLinksHTML(t *testing.T) {
	got := expandWikiLinks("[[file:templates/Rack Plan.html]]", "Server")
	want := "[Rack Plan.html](/wiki/Server/templates/Rack%20Plan.html)"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestSearchFindsHTMLText(t *testing.T) {
	tmp := withTempWiki(t)
	prevIndex := searchIndex
	searchIndex = &SearchIndex{}
	t.Cleanup(func() { searchIndex = prevIndex })
	page := `<html><head><title>x</title><style>.firmware{}</style>
<script>var firmware = 1;</script></head>
<body><!-- firmware --><h1>Rack</h1><p>iDRAC&nbsp;<b>firmware</b> notes &amp; more</p></body></html>`
	if err := os.WriteFile(path.Join(tmp, "wiki", "Server", "rack.html"), []byte(page), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ensureSearchIndexFresh(); err != nil {
		t.Fatal(err)
	}

	results, _ := searchDocs("firmware")
	if len(results) != 1 {
		t.Fatalf("results = %+v, want the page only", results)
	}
	r := results[0]
	if r.Title != "rack" || r.Link != "/wiki/Server/rack.html" || r.Kind != "html" {
		t.Errorf("result = %+v", r)
	}
	if r.RenderedSnippet != "" || !strings.Contains(r.PlainSnippet, "iDRAC firmware notes & more") {
		t.Errorf("snippet: rendered %q, plain %q", r.RenderedSnippet, r.PlainSnippet)
	}
	if strings.Contains(r.PlainSnippet, "var") || strings.Contains(r.PlainSnippet, "<") {
		t.Errorf("snippet leaks markup or script: %q", r.PlainSnippet)
	}
}
