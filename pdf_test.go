package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path"
	"strings"
	"testing"
)

// minimalPDF builds a valid one-page PDF showing text, with a correct xref
// table so pdftotext reads it without repairs.
func minimalPDF(text string) []byte {
	stream := fmt.Sprintf("BT /F1 12 Tf 72 720 Td (%s) Tj ET", text)
	objects := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R /Resources << /Font << /F1 5 0 R >> >> >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(stream), stream),
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
	}
	var b strings.Builder
	b.WriteString("%PDF-1.4\n")
	offsets := make([]int, len(objects))
	for i, obj := range objects {
		offsets[i] = b.Len()
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, obj)
	}
	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(objects)+1)
	for _, off := range offsets {
		fmt.Fprintf(&b, "%010d 00000 n \n", off)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objects)+1, xref)
	return []byte(b.String())
}

func TestHandleWikiAPIPDF(t *testing.T) {
	tmp := withTempWiki(t)
	if err := os.WriteFile(path.Join(tmp, "wiki", "Server", "R450 Manual.pdf"), []byte("%PDF-1.4"), 0o644); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/wiki?path=Server/R450%20Manual.pdf", nil)
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
		Mode:    "pdf",
		Title:   "R450 Manual",
		RelPath: "Server/R450 Manual.pdf",
		FileURL: "/api/download/Server/R450%20Manual.pdf",
	}
	if fmt.Sprintf("%+v", got) != fmt.Sprintf("%+v", want) {
		t.Errorf("response = %+v, want %+v", got, want)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/wiki?path=Server/missing.pdf", nil)
	rec = httptest.NewRecorder()
	HandleWikiAPI(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("missing pdf: status = %d, want 404", rec.Code)
	}
}

func TestSidebarListsPDFs(t *testing.T) {
	tmp := withTempWiki(t)
	for name, body := range map[string]string{"guide.md": "# Guide", "Spec.PDF": "%PDF-1.4", "backup.zip": "PK"} {
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
		{Title: "Guide", Link: "/wiki/Server/guide"},
		{Title: "Spec", Link: "/wiki/Server/Spec.PDF", Kind: "pdf"},
	}
	if fmt.Sprint(items) != fmt.Sprint(want) {
		t.Errorf("sidebar = %+v, want %+v", items, want)
	}
}

func TestSearchFindsPDFText(t *testing.T) {
	if _, err := exec.LookPath("pdftotext"); err != nil {
		t.Skip("pdftotext not installed")
	}
	tmp := withTempWiki(t)
	prevIndex := searchIndex
	searchIndex = &SearchIndex{}
	t.Cleanup(func() { searchIndex = prevIndex })
	if err := os.WriteFile(path.Join(tmp, "wiki", "Server", "datasheet.pdf"), minimalPDF("iDRAC firmware notes"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path.Join(tmp, "wiki", "Server", "guide.md"), []byte("# Guide\n\nNothing here."), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ensureSearchIndexFresh(); err != nil {
		t.Fatal(err)
	}

	results, _ := searchDocs("firmware")
	if len(results) != 1 {
		t.Fatalf("results = %+v, want the datasheet only", results)
	}
	r := results[0]
	if r.Title != "datasheet" || r.Link != "/wiki/Server/datasheet.pdf" || r.Kind != "pdf" {
		t.Errorf("result = %+v", r)
	}
	if r.RenderedSnippet != "" || !strings.Contains(string(r.HighlightedSnippet), "<mark>firmware</mark>") {
		t.Errorf("snippet: rendered %q, highlighted %q", r.RenderedSnippet, r.HighlightedSnippet)
	}

	suggestions := suggestDocs("datash", 5)
	if len(suggestions) != 1 || suggestions[0].Kind != "pdf" {
		t.Errorf("suggestions = %+v", suggestions)
	}
}
