package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"testing"
)

func TestSanitizeWikiRelPathRejectsHidden(t *testing.T) {
	for _, p := range []string{".git", ".git/config", "Server/.hidden.md", "/.git/HEAD"} {
		if _, err := sanitizeWikiRelPath(p); err == nil {
			t.Errorf("sanitizeWikiRelPath(%q) accepted a hidden path", p)
		}
	}
	if got, err := sanitizeWikiRelPath("Server/notes"); err != nil || got != "Server/notes" {
		t.Errorf("sanitizeWikiRelPath(Server/notes) = %q, %v", got, err)
	}
}

// A full clone of the wiki repo has a real .git directory; it must not show up
// anywhere in the wiki.
func TestHiddenEntriesExcluded(t *testing.T) {
	tmp := withTempWiki(t)
	wiki := path.Join(tmp, "wiki")
	for _, dir := range []string{".git/objects", "Server/.cache"} {
		if err := os.MkdirAll(path.Join(wiki, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	files := map[string]string{
		".git/config":        "[core]",
		".git/notes.md":      "# secret",
		"Server/.cache/x.md": "# cached",
		"Server/.DS_Store":   "junk",
		"Server/r450.md":     "# R450",
	}
	for name, body := range files {
		if err := os.WriteFile(path.Join(wiki, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	nav, err := GenerateSidebarContents()
	if err != nil {
		t.Fatal(err)
	}
	for cat, items := range nav {
		if isHiddenWikiEntry(cat.Title) {
			t.Errorf("sidebar contains hidden category %q", cat.Title)
		}
		for _, it := range items {
			if it.Title == ".cache" || it.Title == ".DS_Store" {
				t.Errorf("sidebar contains hidden entry %q", it.Title)
			}
		}
	}

	_, articles, topics, attachments, err := loadDirectoryByRelPath("Server")
	if err != nil {
		t.Fatal(err)
	}
	if len(articles) != 1 || len(topics) != 0 || len(attachments) != 0 {
		t.Errorf("listing = %d articles, %d topics, %d files; want 1, 0, 0", len(articles), len(topics), len(attachments))
	}

	docs, _, err := collectMarkdownDocs(wiki)
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 1 {
		t.Errorf("search indexed %d docs, want 1: %+v", len(docs), docs)
	}

	rec := httptest.NewRecorder()
	HandleDownloadAPI(rec, httptest.NewRequest(http.MethodGet, "/api/download?path=.git/config", nil))
	if rec.Code == http.StatusOK {
		t.Errorf("download of .git/config returned 200")
	}
}
