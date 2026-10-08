package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"testing"
)

// withTempWiki points the package-global cwd at a throwaway tree containing a
// ./wiki directory, then restores it. The download handler resolves files under
// path.Join(cwd, "wiki", ...).
func withTempWiki(t *testing.T) string {
	t.Helper()
	tmp := t.TempDir()
	if err := os.MkdirAll(path.Join(tmp, "wiki", "Server"), 0o755); err != nil {
		t.Fatal(err)
	}
	prev := cwd
	cwd = tmp
	t.Cleanup(func() { cwd = prev })
	return tmp
}

func TestHandleDownloadAPI(t *testing.T) {
	tmp := withTempWiki(t)
	want := []byte("%PDF-1.4 fake datasheet")
	if err := os.WriteFile(path.Join(tmp, "wiki", "Server", "r450.pdf"), want, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path.Join(tmp, "wiki", "Server", "notes.md"), []byte("# hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path.Join(tmp, "wiki", "Server", "backup.zip"), []byte("PK\x03\x04"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path.Join(tmp, "wiki", "Server", "page.html"), []byte("<h1>hi</h1>"), 0o644); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name            string
		path            string
		wantStatus      int
		wantBody        string
		wantDisposition string
		wantType        string
		wantCSP         string
	}{
		{name: "displays pdf inline", path: "Server/r450.pdf", wantStatus: http.StatusOK, wantBody: string(want), wantDisposition: `inline; filename="r450.pdf"`, wantType: "application/pdf"},
		{name: "displays markdown source inline", path: "Server/notes.md", wantStatus: http.StatusOK, wantBody: "# hi", wantDisposition: `inline; filename="notes.md"`, wantType: "text/plain; charset=utf-8"},
		{name: "downloads unknown type", path: "Server/backup.zip", wantStatus: http.StatusOK, wantDisposition: `attachment; filename="backup.zip"`},
		{name: "displays html inline but sandboxed", path: "Server/page.html", wantStatus: http.StatusOK, wantBody: "<h1>hi</h1>", wantDisposition: `inline; filename="page.html"`, wantType: "text/html", wantCSP: htmlSandboxPolicy},
		{name: "rejects traversal", path: "../main.go", wantStatus: http.StatusBadRequest},
		{name: "rejects missing", path: "Server/nope.zip", wantStatus: http.StatusNotFound},
		{name: "rejects directory", path: "Server", wantStatus: http.StatusNotFound},
		{name: "rejects empty", path: "", wantStatus: http.StatusBadRequest},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/download?path="+tc.path, nil)
			rec := httptest.NewRecorder()
			HandleDownloadAPI(rec, req)

			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tc.wantStatus)
			}
			if tc.wantBody != "" && rec.Body.String() != tc.wantBody {
				t.Errorf("body = %q, want %q", rec.Body.String(), tc.wantBody)
			}
			if tc.wantDisposition != "" {
				if got := rec.Header().Get("Content-Disposition"); got != tc.wantDisposition {
					t.Errorf("Content-Disposition = %q, want %q", got, tc.wantDisposition)
				}
			}
			if tc.wantType != "" {
				if got := rec.Header().Get("Content-Type"); got != tc.wantType {
					t.Errorf("Content-Type = %q, want %q", got, tc.wantType)
				}
			}
			if got := rec.Header().Get("Content-Security-Policy"); got != tc.wantCSP {
				t.Errorf("Content-Security-Policy = %q, want %q", got, tc.wantCSP)
			}
		})
	}
}

// HTML pages are embedded by their path-style URL (see fileURL), so that the
// images and stylesheets they reference relatively resolve next to them.
func TestHandleDownloadAPIPathURL(t *testing.T) {
	tmp := withTempWiki(t)
	dir := path.Join(tmp, "wiki", "Server", "templates")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"Rack Plan.html": `<img src="rack.png">`, "rack.png": "PNG"} {
		if err := os.WriteFile(path.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	if got, want := fileURL("Server/templates/Rack Plan.html"), "/api/download/Server/templates/Rack%20Plan.html"; got != want {
		t.Fatalf("fileURL = %q, want %q", got, want)
	}
	cases := []struct {
		url        string
		wantStatus int
		wantBody   string
	}{
		{url: "/api/download/Server/templates/Rack%20Plan.html", wantStatus: http.StatusOK, wantBody: `<img src="rack.png">`},
		{url: "/api/download/Server/templates/rack.png", wantStatus: http.StatusOK, wantBody: "PNG"},
		{url: "/api/download/Server/templates/..%2F..%2F..%2Fmain.go", wantStatus: http.StatusBadRequest},
		{url: "/api/download/", wantStatus: http.StatusBadRequest},
	}
	for _, tc := range cases {
		req := httptest.NewRequest(http.MethodGet, tc.url, nil)
		rec := httptest.NewRecorder()
		HandleDownloadAPI(rec, req)
		if rec.Code != tc.wantStatus {
			t.Errorf("%s: status = %d, want %d", tc.url, rec.Code, tc.wantStatus)
			continue
		}
		if tc.wantBody != "" && rec.Body.String() != tc.wantBody {
			t.Errorf("%s: body = %q, want %q", tc.url, rec.Body.String(), tc.wantBody)
		}
	}
}

func TestExpandWikiLinksFilePrefix(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		baseDir string
		want    string
	}{
		{
			name: "relative to article directory",
			in:   "[[file:test.pdf]]", baseDir: "Test",
			want: "[test.pdf](/wiki/Test/test.pdf)",
		},
		{
			name: "relative from root article",
			in:   "[[file:test.pdf]]", baseDir: "",
			want: "[test.pdf](/wiki/test.pdf)",
		},
		{
			name: "leading slash forces root",
			in:   "[[file:/shared.pdf]]", baseDir: "Test",
			want: "[shared.pdf](/wiki/shared.pdf)",
		},
		{
			name: "parent traversal within root",
			in:   "[[file:../shared.pdf]]", baseDir: "Test/Sub",
			want: "[shared.pdf](/wiki/Test/shared.pdf)",
		},
		{
			name: "nested relative path",
			in:   "[[file:assets/r450.pdf]]", baseDir: "Server",
			want: "[r450.pdf](/wiki/Server/assets/r450.pdf)",
		},
		{
			name: "explicit label wins",
			in:   "[[file:r450.pdf|Datasheet]]", baseDir: "Server",
			want: "[Datasheet](/wiki/Server/r450.pdf)",
		},
		{
			name: "non-pdf attachment downloads",
			in:   "[[file:data/backup.zip]]", baseDir: "Server",
			want: "[backup.zip](/api/download?path=Server%2Fdata%2Fbackup.zip)",
		},
		{
			name: "case-insensitive scheme",
			in:   "[[FILE:notes.txt]]", baseDir: "",
			want: "[notes.txt](/api/download?path=notes.txt)",
		},
		{
			name: "spaces in path are escaped",
			in:   "[[file:data sheet.pdf]]", baseDir: "Server",
			want: "[data sheet.pdf](/wiki/Server/data%20sheet.pdf)",
		},
		{
			name: "escaping root keeps raw text",
			in:   "[[file:../../etc/passwd]]", baseDir: "Test",
			want: "[[file:../../etc/passwd]]",
		},
		{
			name: "empty target keeps raw text",
			in:   "[[file:]]", baseDir: "Test",
			want: "[[file:]]",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := expandWikiLinks(tc.in, tc.baseDir); got != tc.want {
				t.Errorf("expandWikiLinks(%q, %q) = %q, want %q", tc.in, tc.baseDir, got, tc.want)
			}
		})
	}
}

func TestLoadDirectoryByRelPathAttachments(t *testing.T) {
	tmp := withTempWiki(t)
	base := path.Join(tmp, "wiki", "Server")
	if err := os.WriteFile(path.Join(base, "guide.md"), []byte("# Guide"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path.Join(base, "r450.pdf"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path.Join(base, "backup.zip"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, articles, _, files, err := loadDirectoryByRelPath("Server")
	if err != nil {
		t.Fatal(err)
	}
	// The PDF is listed with the articles and opens in the viewer; other
	// attachments stay downloads.
	if len(articles) != 2 {
		t.Fatalf("articles = %+v, want guide and r450", articles)
	}
	pdf := articles[1]
	if pdf.Title != "r450" || pdf.Link != "/wiki/Server/r450.pdf" || pdf.Kind != "pdf" {
		t.Errorf("pdf entry = %+v", pdf)
	}
	if len(files) != 1 {
		t.Fatalf("files = %d, want 1", len(files))
	}
	if files[0].Title != "backup.zip" {
		t.Errorf("file title = %q, want backup.zip", files[0].Title)
	}
	if files[0].Link != "/api/download?path=Server%2Fbackup.zip" {
		t.Errorf("file link = %q", files[0].Link)
	}
}
