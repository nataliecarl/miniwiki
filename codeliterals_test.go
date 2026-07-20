package main

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// Text inside code spans and fenced blocks must survive both substitution
// passes verbatim, so an article can document the syntax it is written in.
func TestCodeLiteralsAreNotSubstituted(t *testing.T) {
	year := fmt.Sprintf("%d", time.Now().Year())

	cases := []struct {
		name     string
		input    string
		contains []string // must appear untouched in the output
		absent   []string
	}{
		{
			name:     "inline code span",
			input:    "Write `[[Kiwi Tips.md]]` to link, or `{-{year}-}` for the year.",
			contains: []string{"`[[Kiwi Tips.md]]`", "`{-{year}-}`"},
			absent:   []string{"/wiki/", year},
		},
		{
			name:     "fenced block",
			input:    "Example:\n\n```markdown\n[[file:Kiwi Tips.md]]\n{-{year}-}\n```\n",
			contains: []string{"[[file:Kiwi Tips.md]]\n{-{year}-}"},
			absent:   []string{"/api/download", year},
		},
		{
			name:     "tilde fence",
			input:    "~~~\n[[Target]]\n~~~\n",
			contains: []string{"[[Target]]"},
			absent:   []string{"/wiki/"},
		},
		{
			name:     "double backtick span holding a backtick",
			input:    "Use ``[[A]] ` `` here.",
			contains: []string{"``[[A]] ` ``"},
			absent:   []string{"/wiki/"},
		},
		{
			name:     "prose outside code still expands",
			input:    "```\n[[Inside]]\n```\n\nSee [[Outside]] in {-{year}-}.",
			contains: []string{"[[Inside]]", "/wiki/Outside", year},
		},
		{
			name:     "unterminated backtick is ordinary text",
			input:    "A stray ` tick and [[Outside]].",
			contains: []string{"/wiki/Outside"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := expandWikiLinks(applyDynamicVars(tc.input), "")
			for _, want := range tc.contains {
				if !strings.Contains(got, want) {
					t.Errorf("output missing %q:\n%s", want, got)
				}
			}
			for _, bad := range tc.absent {
				if strings.Contains(got, bad) {
					t.Errorf("output should not contain %q:\n%s", bad, got)
				}
			}
		})
	}
}

// The wrappers must not disturb documents that contain no code at all.
func TestSubstitutionUnchangedWithoutCode(t *testing.T) {
	in := "# Title\n\nSee [[Target]].\n\n- a\n- b\n"
	if got, want := expandWikiLinks(in, ""), expandWikiLinksText(in, ""); got != want {
		t.Errorf("code-aware pass differs:\n got: %q\nwant: %q", got, want)
	}
}
