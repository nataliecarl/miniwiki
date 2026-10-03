package main

import "testing"

func TestHeadlineFromMarkdown(t *testing.T) {
	cases := []struct {
		name     string
		content  string
		headline string
		found    bool
	}{
		{
			name:     "first level-1 heading wins",
			content:  "# Dell PowerEdge R450 Servers\n\ntext\n\n# Later Heading\n",
			headline: "Dell PowerEdge R450 Servers",
			found:    true,
		},
		{
			name:     "leading prose before heading",
			content:  "intro line\n\n# Onboarding\n",
			headline: "Onboarding",
			found:    true,
		},
		{
			name:    "no heading",
			content: "just text\n",
			found:   false,
		},
		{
			name:    "empty document",
			content: "",
			found:   false,
		},
		{
			name:    "deeper headings are not titles",
			content: "## Section\n### Subsection\n",
			found:   false,
		},
		{
			name:    "hash without space is not a heading",
			content: "#hashtag\n",
			found:   false,
		},
		{
			name:     "closed atx heading",
			content:  "# Printers #\n",
			headline: "Printers",
			found:    true,
		},
		{
			name:     "heading inside fenced code is skipped",
			content:  "```sh\n# not a title, a shell comment\n```\n\n# Real Title\n",
			headline: "Real Title",
			found:    true,
		},
		{
			name:    "fenced code with no heading after it",
			content: "```sh\n# apt install foo\n```\n",
			found:   false,
		},
		{
			name:     "tilde fence is skipped too",
			content:  "~~~\n# fake\n~~~\n# Real\n",
			headline: "Real",
			found:    true,
		},
		{
			name:    "empty heading text is ignored",
			content: "# \n",
			found:   false,
		},
		{
			name:     "dynamic vars are left raw for the caller",
			content:  "# Report {-{year}-}\n",
			headline: "Report {-{year}-}",
			found:    true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			headline, found := headlineFromMarkdown(tc.content)
			if found != tc.found {
				t.Fatalf("found = %v, want %v", found, tc.found)
			}
			if headline != tc.headline {
				t.Errorf("headline = %q, want %q", headline, tc.headline)
			}
		})
	}
}

func TestSuggestionContext(t *testing.T) {
	cases := []struct {
		docPath string
		want    string
	}{
		{"Papers/Reviews/Writing Paper Reviews", "Papers / Reviews"},
		{"meta/Printers", "meta"},
		{"README", ""},
		{"", ""},
	}

	for _, tc := range cases {
		if got := suggestionContext(tc.docPath); got != tc.want {
			t.Errorf("suggestionContext(%q) = %q, want %q", tc.docPath, got, tc.want)
		}
	}
}
