package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gomarkdown/markdown"
	"github.com/gomarkdown/markdown/html"
	"github.com/gomarkdown/markdown/parser"
)

const (
	ParserFlags = parser.CommonExtensions |
		parser.NoEmptyLineBeforeBlock |
		parser.NoIntraEmphasis |
		parser.Tables |
		parser.FencedCode |
		parser.Autolink |
		parser.Strikethrough |
		parser.Footnotes |
		parser.MathJax |
		parser.OrderedListStart |
		parser.SuperSubscript |
		parser.EmptyLinesBreakList
	RendererFlags = html.CommonFlags
)

var (
	htmlRenderer *html.Renderer
	templates    *template.Template
	cwd          string
	searchIndex  = &SearchIndex{}
)

func init() {
	htmlRenderer = html.NewRenderer(html.RendererOptions{
		Flags: RendererFlags,
	})
	templates = template.Must(template.ParseGlob("./templates/*.tmpl"))
	var err error
	cwd, err = os.Getwd()
	if err != nil {
		panic(err.Error())
	}
}

type NavigationElement struct {
	Title string
	Link  string
}

type DirectoryStructure struct {
	Subdirectories []NavigationElement
	Files          []NavigationElement
}

type SearchDoc struct {
	Title             string
	Link              string
	Path              string
	Content           string
	NormalizedTitle   string
	NormalizedPath    string
	NormalizedContent string
}

type SearchResult struct {
	Title              string
	Link               string
	Path               string
	RenderedSnippet    template.HTML
	PlainSnippet       string
	HighlightedSnippet template.HTML
	Score              int
}

type SearchSuggestion struct {
	Title        string `json:"title"`
	Link         string `json:"link"`
	Path         string `json:"path"`
	MatchPreview string `json:"match_preview,omitempty"`
}

type SearchIndex struct {
	mu               sync.RWMutex
	docs             []SearchDoc
	lastIndexedAt    time.Time
	latestContentMod time.Time
	initialized      bool
}

type PageData struct {
	Navigation []NavigationElement
	Article    template.HTML
	Directory  DirectoryStructure
}

func ParseMarkdown(document []byte) []byte {
	p := parser.NewWithExtensions(ParserFlags)
	d := p.Parse(document)
	return markdown.Render(d, htmlRenderer)
}

func main() {
	fs := http.FileServer(http.Dir("./static"))

	http.HandleFunc("/", handleIndex)
	http.HandleFunc("/search", HandleSearch)
	http.HandleFunc("/search/suggest", HandleSearchSuggest)
	http.HandleFunc("/wiki/", MakeHandler(HandleView))
	http.Handle("/static/", http.StripPrefix("/static/", fs))

	http.ListenAndServe(":8080", nil)
}

func handleIndex(w http.ResponseWriter, r *http.Request) {
	wikiContent, err := os.ReadDir("./wiki")
	if err != nil {
		panic(err)
	}
	categories := make([]NavigationElement, 0)
	articles := make([]NavigationElement, 0)
	for _, x := range wikiContent {
		title := x.Name()
		if x.IsDir() {
			categories = append(categories, NavigationElement{
				Title: title,
				Link:  fmt.Sprintf("/wiki/%s", title),
			})
		} else {
			if strings.HasSuffix(title, ".md") {
				title = title[:len(title)-3]
				articles = append(articles, NavigationElement{
					Title: title,
					Link:  fmt.Sprintf("/wiki/%s", title),
				})
			}
		}
	}
	err = templates.ExecuteTemplate(w, "index.tmpl", struct {
		Subdirectories []NavigationElement
		Files          []NavigationElement
	}{categories, articles})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// article | is path if nested, else just the document title (without file extension)
func HandleView(w http.ResponseWriter, r *http.Request, relPath string) {
	absPath := path.Join(cwd, "wiki", relPath)
	fi, err := os.Stat(absPath)

	//log.Printf("a: %s, r: %s", absPath, relPath)

	// try if that's a directory we can use
	if err == nil && fi.IsDir() {
		RenderDirectory(w, r, relPath, absPath)
	} else {
		RenderArticle(w, r, absPath)
	}
}

type Data struct {
	Title      string
	Mode       string
	Navigation map[NavigationElement][]NavigationElement
	Content    template.HTML
	Directory  struct {
		Articles []NavigationElement
		Topics   []NavigationElement
	}
	Search struct {
		Query      string
		HasQuery   bool
		Results    []SearchResult
		IndexedAt  time.Time
		TotalCount int
	}
}

func normalizeForSearch(s string) string {
	return strings.ToLower(strings.Join(strings.Fields(s), " "))
}

func buildWikiLinkFromAbsPath(absPath string) (string, string) {
	wikiRoot := path.Join(cwd, "wiki")
	relPath, err := filepath.Rel(wikiRoot, absPath)
	if err != nil {
		return "", ""
	}
	relPath = filepath.ToSlash(relPath)
	if !strings.HasSuffix(relPath, ".md") {
		return "", ""
	}
	withoutExt := strings.TrimSuffix(relPath, ".md")
	return withoutExt, path.Join("/wiki", withoutExt)
}

func latestWikiMarkdownModTime(root string) (time.Time, error) {
	latest := time.Time{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".md") {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if info.ModTime().After(latest) {
			latest = info.ModTime()
		}
		return nil
	})
	if err != nil {
		return time.Time{}, err
	}
	return latest, nil
}

func collectMarkdownDocs(root string) ([]SearchDoc, time.Time, error) {
	docs := make([]SearchDoc, 0)
	latest := time.Time{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".md") {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if info.ModTime().After(latest) {
			latest = info.ModTime()
		}
		body, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		relPath, link := buildWikiLinkFromAbsPath(p)
		if relPath == "" {
			return nil
		}
		title := strings.TrimSuffix(path.Base(relPath), ".md")
		content := string(body)
		docs = append(docs, SearchDoc{
			Title:             title,
			Link:              link,
			Path:              relPath,
			Content:           content,
			NormalizedTitle:   normalizeForSearch(title),
			NormalizedPath:    normalizeForSearch(relPath),
			NormalizedContent: normalizeForSearch(content),
		})
		return nil
	})
	if err != nil {
		return nil, time.Time{}, err
	}
	sort.Slice(docs, func(i, j int) bool {
		return docs[i].Path < docs[j].Path
	})
	return docs, latest, nil
}

func ensureSearchIndexFresh() error {
	root := path.Join(cwd, "wiki")
	latest, err := latestWikiMarkdownModTime(root)
	if err != nil {
		return err
	}
	searchIndex.mu.RLock()
	initialized := searchIndex.initialized
	latestIndexed := searchIndex.latestContentMod
	searchIndex.mu.RUnlock()
	if initialized && latest.Equal(latestIndexed) {
		return nil
	}
	docs, latestContentMod, err := collectMarkdownDocs(root)
	if err != nil {
		return err
	}
	searchIndex.mu.Lock()
	searchIndex.docs = docs
	searchIndex.lastIndexedAt = time.Now()
	searchIndex.latestContentMod = latestContentMod
	searchIndex.initialized = true
	searchIndex.mu.Unlock()
	return nil
}

func findMatchIndex(haystack, needle []rune) int {
	if len(needle) == 0 || len(haystack) < len(needle) {
		return -1
	}
	for i := 0; i <= len(haystack)-len(needle); i++ {
		match := true
		for j := range needle {
			if haystack[i+j] != needle[j] {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}

func makeSearchSnippet(content, query string) string {
	const maxRunes = 180
	text := strings.Join(strings.Fields(content), " ")
	if text == "" {
		return ""
	}
	textRunes := []rune(text)
	if len(textRunes) <= maxRunes {
		return text
	}
	queryRunes := []rune(strings.ToLower(strings.TrimSpace(query)))
	lowerRunes := []rune(strings.ToLower(text))
	match := findMatchIndex(lowerRunes, queryRunes)
	if match < 0 {
		snippet := string(textRunes[:maxRunes])
		return snippet + "..."
	}
	start := match - (maxRunes / 3)
	if start < 0 {
		start = 0
	}
	end := start + maxRunes
	if end > len(textRunes) {
		end = len(textRunes)
		start = end - maxRunes
		if start < 0 {
			start = 0
		}
	}
	snippet := string(textRunes[start:end])
	if start > 0 {
		snippet = "..." + snippet
	}
	if end < len(textRunes) {
		snippet += "..."
	}
	return snippet
}

func highlightSearchPhrase(snippet, query string) template.HTML {
	escaped := template.HTMLEscapeString(snippet)
	trimmedQuery := strings.TrimSpace(query)
	if trimmedQuery == "" {
		return template.HTML(escaped)
	}
	re, err := regexp.Compile("(?i)" + regexp.QuoteMeta(trimmedQuery))
	if err != nil {
		return template.HTML(escaped)
	}
	highlighted := re.ReplaceAllStringFunc(escaped, func(match string) string {
		return "<mark>" + match + "</mark>"
	})
	return template.HTML(highlighted)
}

func makeRenderedSearchSnippet(content, query string) template.HTML {
	const fragmentRunes = 520
	trimmedQuery := strings.TrimSpace(query)
	if trimmedQuery == "" {
		rendered := string(ParseMarkdown([]byte(content)))
		return template.HTML(rendered)
	}

	contentRunes := []rune(content)
	if len(contentRunes) == 0 {
		return template.HTML("")
	}

	match := findMatchIndex([]rune(strings.ToLower(content)), []rune(strings.ToLower(trimmedQuery)))
	start := 0
	end := len(contentRunes)
	if match >= 0 {
		start = match - (fragmentRunes / 2)
		if start < 0 {
			start = 0
		}
		end = start + fragmentRunes
		if end > len(contentRunes) {
			end = len(contentRunes)
			start = end - fragmentRunes
			if start < 0 {
				start = 0
			}
		}
	} else if len(contentRunes) > fragmentRunes {
		end = fragmentRunes
	}

	fragment := string(contentRunes[start:end])
	if start > 0 {
		fragment = "...\n\n" + fragment
	}
	if end < len(contentRunes) {
		fragment += "\n\n..."
	}

	rendered := string(ParseMarkdown([]byte(fragment)))
	return highlightRenderedHTMLText(rendered, trimmedQuery)
}

func highlightRenderedHTMLText(rendered, query string) template.HTML {
	if strings.TrimSpace(query) == "" {
		return template.HTML(rendered)
	}
	var out strings.Builder
	segment := strings.Builder{}
	inTag := false
	for _, r := range rendered {
		if r == '<' {
			if segment.Len() > 0 {
				out.WriteString(highlightInTextSegment(segment.String(), query))
				segment.Reset()
			}
			inTag = true
			out.WriteRune(r)
			continue
		}
		if r == '>' {
			inTag = false
			out.WriteRune(r)
			continue
		}
		if inTag {
			out.WriteRune(r)
		} else {
			segment.WriteRune(r)
		}
	}
	if segment.Len() > 0 {
		out.WriteString(highlightInTextSegment(segment.String(), query))
	}
	return template.HTML(out.String())
}

func highlightInTextSegment(text, query string) string {
	lowerText := strings.ToLower(text)
	lowerQuery := strings.ToLower(query)
	if lowerQuery == "" || !strings.Contains(lowerText, lowerQuery) {
		return text
	}
	queryRunes := []rune(lowerQuery)
	textRunes := []rune(text)
	lowerRunes := []rune(lowerText)
	var out strings.Builder
	searchStart := 0
	for searchStart < len(textRunes) {
		next := findMatchIndex(lowerRunes[searchStart:], queryRunes)
		if next < 0 {
			out.WriteString(string(textRunes[searchStart:]))
			break
		}
		matchStart := searchStart + next
		matchEnd := matchStart + len(queryRunes)
		out.WriteString(string(textRunes[searchStart:matchStart]))
		out.WriteString("<mark>")
		out.WriteString(string(textRunes[matchStart:matchEnd]))
		out.WriteString("</mark>")
		searchStart = matchEnd
	}
	return out.String()
}

func searchDocs(query string) ([]SearchResult, time.Time) {
	normQuery := normalizeForSearch(query)
	if normQuery == "" {
		searchIndex.mu.RLock()
		indexedAt := searchIndex.lastIndexedAt
		searchIndex.mu.RUnlock()
		return nil, indexedAt
	}
	searchIndex.mu.RLock()
	docs := make([]SearchDoc, len(searchIndex.docs))
	copy(docs, searchIndex.docs)
	indexedAt := searchIndex.lastIndexedAt
	searchIndex.mu.RUnlock()
	results := make([]SearchResult, 0)
	for _, doc := range docs {
		titleHit := strings.Contains(doc.NormalizedTitle, normQuery)
		pathHit := strings.Contains(doc.NormalizedPath, normQuery)
		contentHit := strings.Contains(doc.NormalizedContent, normQuery)
		if !(titleHit || pathHit || contentHit) {
			continue
		}
		score := 0
		if titleHit {
			score += 100
		}
		if pathHit {
			score += 50
		}
		if contentHit {
			score += 20
		}
		snippet := makeSearchSnippet(doc.Content, query)
		results = append(results, SearchResult{
			Title:              doc.Title,
			Link:               doc.Link,
			Path:               doc.Path,
			RenderedSnippet:    makeRenderedSearchSnippet(doc.Content, query),
			PlainSnippet:       snippet,
			HighlightedSnippet: highlightSearchPhrase(snippet, query),
			Score:              score,
		})
	}
	sort.Slice(results, func(i, j int) bool {
		if results[i].Score == results[j].Score {
			return results[i].Path < results[j].Path
		}
		return results[i].Score > results[j].Score
	})
	return results, indexedAt
}

func suggestDocs(query string, limit int) []SearchSuggestion {
	normQuery := normalizeForSearch(query)
	if normQuery == "" {
		return []SearchSuggestion{}
	}
	searchIndex.mu.RLock()
	docs := make([]SearchDoc, len(searchIndex.docs))
	copy(docs, searchIndex.docs)
	searchIndex.mu.RUnlock()

	type scoredSuggestion struct {
		SearchSuggestion
		score int
	}
	matches := make([]scoredSuggestion, 0)
	for _, doc := range docs {
		titleHas := strings.Contains(doc.NormalizedTitle, normQuery)
		pathHas := strings.Contains(doc.NormalizedPath, normQuery)
		contentHas := strings.Contains(doc.NormalizedContent, normQuery)
		if !titleHas && !pathHas && !contentHas {
			continue
		}
		score := 0
		if strings.HasPrefix(doc.NormalizedTitle, normQuery) {
			score += 140
		}
		if strings.HasPrefix(doc.NormalizedPath, normQuery) {
			score += 100
		}
		if titleHas {
			score += 40
		}
		if pathHas {
			score += 20
		}
		preview := ""
		if contentHas {
			score += 10
			preview = makeSuggestionPreview(doc.Content, query)
		}
		matches = append(matches, scoredSuggestion{
			SearchSuggestion: SearchSuggestion{
				Title:        doc.Title,
				Link:         doc.Link,
				Path:         doc.Path,
				MatchPreview: preview,
			},
			score: score,
		})
	}
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].score == matches[j].score {
			return matches[i].Path < matches[j].Path
		}
		return matches[i].score > matches[j].score
	})
	if len(matches) > limit {
		matches = matches[:limit]
	}
	out := make([]SearchSuggestion, 0, len(matches))
	for _, match := range matches {
		out = append(out, match.SearchSuggestion)
	}
	return out
}

func makeSuggestionPreview(content, query string) string {
	const maxRunes = 110
	condensed := strings.Join(strings.Fields(content), " ")
	if condensed == "" {
		return ""
	}
	condensedRunes := []rune(condensed)
	if len(condensedRunes) <= maxRunes {
		return condensed
	}
	queryRunes := []rune(strings.ToLower(strings.TrimSpace(query)))
	lowerRunes := []rune(strings.ToLower(condensed))
	match := findMatchIndex(lowerRunes, queryRunes)
	if match < 0 {
		return string(condensedRunes[:maxRunes]) + "..."
	}
	start := match - (maxRunes / 3)
	if start < 0 {
		start = 0
	}
	end := start + maxRunes
	if end > len(condensedRunes) {
		end = len(condensedRunes)
		start = end - maxRunes
		if start < 0 {
			start = 0
		}
	}
	preview := string(condensedRunes[start:end])
	if start > 0 {
		preview = "..." + preview
	}
	if end < len(condensedRunes) {
		preview += "..."
	}
	return preview
}

func HandleSearch(w http.ResponseWriter, r *http.Request) {
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if err := ensureSearchIndexFresh(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	nav, err := GenerateSidebarContents()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	results, indexedAt := searchDocs(query)
	data := Data{
		Title:      "Search",
		Mode:       "search",
		Navigation: nav,
	}
	data.Search.Query = query
	data.Search.HasQuery = query != ""
	data.Search.Results = results
	data.Search.IndexedAt = indexedAt
	data.Search.TotalCount = len(results)
	if err := templates.ExecuteTemplate(w, "layout.tmpl", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func HandleSearchSuggest(w http.ResponseWriter, r *http.Request) {
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if err := ensureSearchIndexFresh(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	suggestions := suggestDocs(query, 8)
	if err := json.NewEncoder(w).Encode(suggestions); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func applyDynamicVars(s string) string {
	now := time.Now()
	s = staticVars(s)
	relativeYear := func(monthStr string) int {
		month, err := monthStringToInt(monthStr)
		if err != nil {
			return 0
		}

		if int(now.Month()) > month {
			return now.Year() + 1
		} else {
			return now.Year()
		}
	}
	re := regexp.MustCompile(`\{\-\{dynamicyear:([a-zA-Z]+)\}\-\}`)
	s = re.ReplaceAllStringFunc(s, func(match string) string {
		monthStr := re.FindStringSubmatch(match)[1]
		relYear := relativeYear(monthStr)
		return fmt.Sprintf("%d", relYear)
	})

	return s
}

func staticVars(s string) string {
	now := time.Now()
	return strings.NewReplacer(
		"{-{year}-}", fmt.Sprintf("%d", now.Year()),
		"{-{year+}-}", fmt.Sprintf("%d", now.Year()+1),
		"{-{year-}-}", fmt.Sprintf("%d", now.Year()-1),
		"{-{month}-}", fmt.Sprintf("%02d", int(now.Month())),
		"{-{namedmonth}-}", fmt.Sprintf("%s", monthIntToString(int(now.Month()))),
		"{-{namedmonthshort}-}", fmt.Sprintf("%s", strings.ToLower(monthIntToString(int(now.Month())))[0:3]),
	).Replace(s)
}

func monthIntToString(month int) string {
	months := map[int]string{1: "January", 2: "February", 3: "March", 4: "April", 5: "May", 6: "June", 7: "July", 8: "August", 9: "September", 10: "October", 11: "November", 12: "December"}
	if monthStr, exists := months[month]; exists {
		return monthStr
	}
	return ""
}

func monthStringToInt(monthStr string) (int, error) {
	months := map[string]int{"jan": 1, "feb": 2, "mar": 3, "apr": 4, "may": 5, "jun": 6, "jul": 7, "aug": 8, "sep": 9, "oct": 10, "nov": 11, "dec": 12}
	monthStr = strings.ToLower(monthStr) // Ensure case-insensitivity
	if month, exists := months[monthStr]; exists {
		return month, nil
	}
	return 0, fmt.Errorf("invalid month: %s", monthStr)
}

func RenderArticle(w http.ResponseWriter, r *http.Request, absPath string) {

	// 1) title
	if absPath[len(absPath)-1:] == "/" {
		absPath = absPath[:len(absPath)-1]
	}
	split := strings.Split(absPath, "/")
	title := split[len(split)-1]

	// 2) content
	absPath += ".md"
	f, err := os.ReadFile(absPath)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	md := applyDynamicVars(string(f))
	content := template.HTML(ParseMarkdown([]byte(md)))

	// 3) navigation
	nav, err := GenerateSidebarContents()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// render
	err = templates.ExecuteTemplate(w, "layout.tmpl", Data{
		Title:      title,
		Mode:       "article",
		Navigation: nav,
		Content:    content,
	})
	if err != nil {
		println(err.Error())
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func RenderDirectory(w http.ResponseWriter, r *http.Request, relPath, absPath string) {

	//log.Printf("a: %s, r: %s", absPath, relPath)

	p := strings.Split(absPath, "/")
	title := p[len(p)-1]

	d, err := os.ReadDir(absPath)
	if err != nil {
		//http.Error(w, err.Error(), http.StatusInternalServerError)
		http.NotFound(w, r)
		return
	}

	contents := struct {
		Articles []NavigationElement
		Topics   []NavigationElement
	}{
		Articles: make([]NavigationElement, 0),
		Topics:   make([]NavigationElement, 0),
	}

	for _, el := range d {
		if el.IsDir() {
			contents.Topics = append(contents.Topics, NavigationElement{
				Title: el.Name(),
				Link:  path.Join("/", "wiki", relPath, el.Name()),
			})
		} else {
			if el.Name()[len(el.Name())-3:] != ".md" {
				continue
			}
			contents.Articles = append(contents.Articles, NavigationElement{
				Title: el.Name()[:len(el.Name())-3],
				Link:  path.Join("/", "wiki", relPath, el.Name()[:len(el.Name())-3]), // this should have the file ending ".md"
			})
		}
	}

	nav, err := GenerateSidebarContents()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	err = templates.ExecuteTemplate(w, "layout.tmpl", Data{
		Title:      title,
		Mode:       "directory",
		Navigation: nav,
		Directory:  contents,
	})
	if err != nil {
		println(err.Error())
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
}

func GenerateSidebarContents() (map[NavigationElement][]NavigationElement, error) {
	// TODO alphabetisch sortiert statt map wär cool aber später; other als letztes
	result := make(map[NavigationElement][]NavigationElement)
	d, err := os.ReadDir(path.Join(cwd, "wiki"))
	if err != nil {
		return nil, errors.New(err.Error())
	}
	other := NavigationElement{
		Title: "other",
		Link:  "/", // TODO all unordered links go back to index
	}
	result[other] = make([]NavigationElement, 0)
	for _, dirEntry := range d {
		title := dirEntry.Name()
		link := fmt.Sprintf("/wiki/%s", title)
		if dirEntry.IsDir() {
			e := NavigationElement{
				Title: title,
				Link:  link,
			}
			result[e] = make([]NavigationElement, 0)
			p := path.Join(cwd, "wiki", title)
			subdir, err := os.ReadDir(p)
			if err != nil {
				return nil, err
			}
			for _, de := range subdir {
				deTitle := de.Name()
				if !de.IsDir() {
					if deTitle[len(deTitle)-3:] != ".md" {
						continue
					}
					deTitle = deTitle[:len(deTitle)-3] // remove file extension
				}
				result[e] = append(result[e], NavigationElement{
					Title: deTitle,
					Link:  fmt.Sprintf("/wiki/%s/%s", title, deTitle),
				})
			}
		} else {
			if title[len(title)-3:] != ".md" {
				continue
			}
			title = title[:len(title)-3] // remove file extension
			result[other] = append(result[other], NavigationElement{
				Title: title,
				Link:  link,
			})
		}
	}
	return result, nil
}

/*
// NEEDSFIX
func RenderDirectory(w http.ResponseWriter, tmpl string, wikiPath string) {
	// get content
	absPath := path.Join(cwd, wikiPath)
	dirEntries, err := os.ReadDir(absPath)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	files := make([]NavigationElement, 0)
	subdirs := make([]NavigationElement, 0)
	for _, de := range dirEntries {
		if de.IsDir() {
			title := de.Name()
			link := fmt.Sprintf("/%s/%s", wikiPath, title)
			subdirs = append(subdirs, NavigationElement{
				Title: title,
				Link:  link,
			})
		} else {
			parts := strings.Split(de.Name(), ".")
			filename := ""
			for i, s := range parts {
				if i == len(parts)-1 {
					break
				}
				filename += s
			}
			files = append(files, NavigationElement{
				Title: filename,
				Link:  fmt.Sprintf("/%s/%s", wikiPath, filename),
			})
		}
	}
	// render template + send output
	err = templates.ExecuteTemplate(w, "layout.tmpl", PageData{
		Navigation: GenerateNavigation(wikiPath),
		Article:    "",
		Directory: DirectoryStructure{
			Subdirectories: subdirs,
			Files:          files,
		},
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
}
*/

/*
// DEPRECATED
func RenderArticle(w http.ResponseWriter, wikiPath string, content string) {
	// TODO create the content here, parsing etc.
	err := templates.ExecuteTemplate(w, "layout.tmpl", PageData{
		Navigation: GenerateNavigation(wikiPath),
		Article:    template.HTML(content),
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
*/

// DEPRECATED
func GenerateNavigation(wikiPath string) []NavigationElement {
	result := make([]NavigationElement, 0)
	parts := strings.Split(wikiPath, "/")
	for i, s := range parts {
		if i == 0 {
			continue
		}
		l := "/"
		for j := range parts {
			if j == i {
				break
			}
			l += parts[j] + "/"
		}
		l += s
		ne := NavigationElement{
			Title: s,
			Link:  l,
		}
		result = append(result, ne)
	}
	return result
}

var validPath = regexp.MustCompile("^/wiki/([a-zA-Z0-9/_ ]+)$")

func MakeHandler(fn func(http.ResponseWriter, *http.Request, string)) http.HandlerFunc {
	// source: https://go.dev/doc/articles/wiki/
	return func(w http.ResponseWriter, r *http.Request) {
		// TODO log for all requests here
		m := validPath.FindStringSubmatch(r.URL.Path)
		if m == nil {
			// if no article or category was requested, send back to index
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}
		fn(w, r, m[1])
	}
}
