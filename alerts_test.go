package main

import (
	"strings"
	"testing"
)

func TestAlertsRender(t *testing.T) {
	cases := []struct {
		name     string
		src      string
		want     []string // substrings that must be present
		notWant  []string // substrings that must be absent
	}{
		{
			name: "single note",
			src:  "> [!NOTE]\n> Useful information.\n",
			want: []string{
				`<div class="markdown-alert markdown-alert-note">`,
				`<p class="markdown-alert-title">`,
				`>Note</p>`,
				`<p>Useful information.</p>`,
			},
			notWant: []string{"[!NOTE]", "<blockquote>"},
		},
		{
			name: "all five stacked split into separate callouts",
			src: "> [!NOTE]\n> n\n\n> [!TIP]\n> t\n\n> [!IMPORTANT]\n> i\n\n" +
				"> [!WARNING]\n> w\n\n> [!CAUTION]\n> c\n",
			want: []string{
				"markdown-alert-note", "markdown-alert-tip", "markdown-alert-important",
				"markdown-alert-warning", "markdown-alert-caution",
			},
			notWant: []string{"[!", "<blockquote>"},
		},
		{
			name: "multi-paragraph body preserved",
			src:  "> [!IMPORTANT]\n> First.\n>\n> Second **bold**.\n",
			want: []string{"<p>First.</p>", "<strong>bold</strong>"},
		},
		{
			name:    "plain blockquote untouched",
			src:     "> just a quote\n",
			want:    []string{"<blockquote>", "just a quote"},
			notWant: []string{"markdown-alert"},
		},
		{
			name:    "unknown type is not an alert",
			src:     "> [!BOGUS]\n> nope\n",
			want:    []string{"<blockquote>", "[!BOGUS]"},
			notWant: []string{"markdown-alert"},
		},
		{
			name:    "marker not alone on line is not an alert",
			src:     "> [!TIP] trailing text\n",
			want:    []string{"<blockquote>", "[!TIP] trailing text"},
			notWant: []string{"markdown-alert"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := string(ParseMarkdown([]byte(tc.src)))
			for _, w := range tc.want {
				if !strings.Contains(got, w) {
					t.Errorf("missing %q in:\n%s", w, got)
				}
			}
			for _, nw := range tc.notWant {
				if strings.Contains(got, nw) {
					t.Errorf("unexpected %q in:\n%s", nw, got)
				}
			}
		})
	}
}
