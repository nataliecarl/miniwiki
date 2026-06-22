package main

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	stdhtml "html"
	"html/template"
	"io"
	"io/fs"
	"log"
	"math/big"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gomarkdown/markdown"
	"github.com/gomarkdown/markdown/ast"
	"github.com/gomarkdown/markdown/html"
	"github.com/gomarkdown/markdown/parser"
)

const (
	DefaultAddr    = ":8080"
	DefaultCertDir = "./certs"
	DefaultCert    = "selfsigned.crt"
	DefaultKey     = "selfsigned.key"

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
	htmlRenderer        *html.Renderer
	templates           *template.Template
	cwd                 string
	searchIndex         = &SearchIndex{}
	wikiLinkRe          = regexp.MustCompile(`\[\[([^\[\]]+)\]\]`)
	renderedBlockTagRe  = regexp.MustCompile(`(?i)</?(?:p|div|h[1-6]|li|ul|ol|blockquote|pre|code|br|tr|td|th)[^>]*>`)
	renderedAnyTagRe    = regexp.MustCompile(`(?s)<[^>]+>`)
	heavySnippetTableRe = regexp.MustCompile(`(?m)^\s*\|.*\|\s*$`)
	heavySnippetFenceRe = regexp.MustCompile("(?m)^\\s*```")
	heavySnippetHTMLRe  = regexp.MustCompile(`(?i)<\s*(?:table|div|aside|details|blockquote|pre)\b`)
)

func init() {
	htmlRenderer = html.NewRenderer(html.RendererOptions{
		Flags:          RendererFlags,
		RenderNodeHook: renderAlert,
	})
	templates = template.Must(template.ParseGlob("./templates/*.tmpl"))
	var err error
	cwd, err = os.Getwd()
	if err != nil {
		panic(err.Error())
	}
}

type NavigationElement struct {
	Title string `json:"title"`
	Link  string `json:"link"`
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
	Category     string `json:"category"`
	MatchPreview string `json:"match_preview,omitempty"`
}

type APINavigationSection struct {
	Title string              `json:"title"`
	Link  string              `json:"link"`
	Items []NavigationElement `json:"items"`
}

type APIHomeResponse struct {
	Categories []NavigationElement `json:"categories"`
	Articles   []NavigationElement `json:"articles"`
	Landing    struct {
		Title       string `json:"title"`
		ContentHTML string `json:"content_html"`
		Link        string `json:"link"`
	} `json:"landing"`
}

type APIWikiResponse struct {
	Mode        string              `json:"mode"`
	Title       string              `json:"title"`
	ContentHTML string              `json:"content_html,omitempty"`
	Content     string              `json:"content,omitempty"`
	Articles    []NavigationElement `json:"articles,omitempty"`
	Topics      []NavigationElement `json:"topics,omitempty"`
	RelPath     string              `json:"rel_path,omitempty"`
	UpdatedAt   string              `json:"updated_at,omitempty"`
}

type APISearchResult struct {
	Title            string `json:"title"`
	Link             string `json:"link"`
	Path             string `json:"path"`
	RenderedSnippet  string `json:"rendered_snippet,omitempty"`
	PlainSnippet     string `json:"plain_snippet,omitempty"`
	HighlightedPlain string `json:"highlighted_plain,omitempty"`
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
	document = []byte(expandWikiLinks(string(document)))
	p := parser.NewWithExtensions(ParserFlags)
	d := p.Parse(document)
	transformAlerts(d)
	return markdown.Render(d, htmlRenderer)
}

// GitHub-style alerts (admonitions): blockquotes that begin with `[!NOTE]`,
// `[!TIP]`, `[!IMPORTANT]`, `[!WARNING]` or `[!CAUTION]` render as titled,
// colour-coded callout boxes. See
// https://github.com/orgs/community/discussions/16925
type alertKind struct {
	slug  string // CSS modifier, e.g. "note"
	title string // displayed title, e.g. "Note"
	icon  string // inline Octicon SVG markup
}

// alertKinds maps the upper-case marker name to its presentation. The Octicon
// SVG paths match the ones GitHub ships for each alert type.
var alertKinds = map[string]alertKind{
	"NOTE":      {"note", "Note", `<svg class="markdown-alert-icon octicon" viewBox="0 0 16 16" width="16" height="16" aria-hidden="true"><path d="M0 8a8 8 0 1 1 16 0A8 8 0 0 1 0 8Zm8-6.5a6.5 6.5 0 1 0 0 13 6.5 6.5 0 0 0 0-13ZM6.5 7.75A.75.75 0 0 1 7.25 7h1a.75.75 0 0 1 .75.75v2.75h.25a.75.75 0 0 1 0 1.5h-2a.75.75 0 0 1 0-1.5h.25v-2h-.25a.75.75 0 0 1-.75-.75ZM8 6a1 1 0 1 1 0-2 1 1 0 0 1 0 2Z"></path></svg>`},
	"TIP":       {"tip", "Tip", `<svg class="markdown-alert-icon octicon" viewBox="0 0 16 16" width="16" height="16" aria-hidden="true"><path d="M8 1.5c-2.363 0-4 1.69-4 3.75 0 .984.424 1.625.984 2.304l.214.253c.223.264.47.556.673.848.284.411.537.896.621 1.49a.75.75 0 0 1-1.484.211c-.04-.282-.163-.547-.37-.847a8.456 8.456 0 0 0-.542-.68c-.084-.1-.173-.205-.268-.32C3.201 7.75 2.5 6.766 2.5 5.25 2.5 2.31 4.863 0 8 0s5.5 2.31 5.5 5.25c0 1.516-.701 2.5-1.328 3.259-.095.115-.184.22-.268.319-.207.245-.383.453-.541.681-.208.3-.33.565-.37.847a.751.751 0 0 1-1.485-.212c.084-.593.337-1.078.621-1.489.203-.292.45-.584.673-.848.075-.088.147-.173.213-.253.561-.679.985-1.32.985-2.304 0-2.06-1.637-3.75-4-3.75ZM5.75 12h4.5a.75.75 0 0 1 0 1.5h-4.5a.75.75 0 0 1 0-1.5ZM6 15.25a.75.75 0 0 1 .75-.75h2.5a.75.75 0 0 1 0 1.5h-2.5a.75.75 0 0 1-.75-.75Z"></path></svg>`},
	"IMPORTANT": {"important", "Important", `<svg class="markdown-alert-icon octicon" viewBox="0 0 16 16" width="16" height="16" aria-hidden="true"><path d="M0 1.75C0 .784.784 0 1.75 0h12.5C15.216 0 16 .784 16 1.75v9.5A1.75 1.75 0 0 1 14.25 13H8.06l-2.573 2.573A1.458 1.458 0 0 1 3 14.543V13H1.75A1.75 1.75 0 0 1 0 11.25Zm1.75-.25a.25.25 0 0 0-.25.25v9.5c0 .138.112.25.25.25h2a.75.75 0 0 1 .75.75v2.19l2.72-2.72a.749.749 0 0 1 .53-.22h6.5a.25.25 0 0 0 .25-.25v-9.5a.25.25 0 0 0-.25-.25Zm7 2.25v2.5a.75.75 0 0 1-1.5 0v-2.5a.75.75 0 0 1 1.5 0ZM9 9a1 1 0 1 1-2 0 1 1 0 0 1 2 0Z"></path></svg>`},
	"WARNING":   {"warning", "Warning", `<svg class="markdown-alert-icon octicon" viewBox="0 0 16 16" width="16" height="16" aria-hidden="true"><path d="M6.457 1.047c.659-1.234 2.427-1.234 3.086 0l6.082 11.378A1.75 1.75 0 0 1 14.082 15H1.918a1.75 1.75 0 0 1-1.543-2.575Zm1.763.707a.25.25 0 0 0-.44 0L1.698 13.132a.25.25 0 0 0 .22.368h12.164a.25.25 0 0 0 .22-.368Zm.53 3.996v2.5a.75.75 0 0 1-1.5 0v-2.5a.75.75 0 0 1 1.5 0ZM9 11a1 1 0 1 1-2 0 1 1 0 0 1 2 0Z"></path></svg>`},
	"CAUTION":   {"caution", "Caution", `<svg class="markdown-alert-icon octicon" viewBox="0 0 16 16" width="16" height="16" aria-hidden="true"><path d="M4.47.22A.749.749 0 0 1 5 0h6c.199 0 .389.079.53.22l4.25 4.25c.141.141.22.331.22.53v6a.749.749 0 0 1-.22.53l-4.25 4.25A.749.749 0 0 1 11 16H5a.749.749 0 0 1-.53-.22L.22 11.53A.749.749 0 0 1 0 11V5c0-.199.079-.389.22-.53Zm.84 1.28L1.5 5.31v5.38l3.81 3.81h5.38l3.81-3.81V5.31L10.69 1.5ZM8 4a.75.75 0 0 1 .75.75v3.5a.75.75 0 0 1-1.5 0v-3.5A.75.75 0 0 1 8 4Zm0 8a1 1 0 1 1 0-2 1 1 0 0 1 0 2Z"></path></svg>`},
}

// AlertBlock is a custom AST node that renders as a GitHub-style alert callout.
type AlertBlock struct {
	ast.Container
	Kind alertKind
}

// alertMarker reports whether para begins with a valid `[!TYPE]` marker that is
// alone on its line. It does not mutate the node.
func alertMarker(para *ast.Paragraph) (alertKind, bool) {
	kids := para.GetChildren()
	if len(kids) == 0 {
		return alertKind{}, false
	}
	txt, ok := kids[0].(*ast.Text)
	if !ok {
		return alertKind{}, false
	}
	lit := txt.Literal
	if len(lit) < 4 || lit[0] != '[' || lit[1] != '!' {
		return alertKind{}, false
	}
	end := bytes.IndexByte(lit, ']')
	if end < 0 {
		return alertKind{}, false
	}
	kind, ok := alertKinds[string(lit[2:end])]
	if !ok {
		return alertKind{}, false
	}
	// The marker must stand alone: only trailing whitespace before the line end.
	rest := lit[end+1:]
	i := 0
	for i < len(rest) && (rest[i] == ' ' || rest[i] == '\t') {
		i++
	}
	if i < len(rest) && rest[i] != '\n' {
		return alertKind{}, false
	}
	return kind, true
}

// stripMarker removes the `[!TYPE]` marker (and its trailing newline) from the
// paragraph's leading text node. It reports whether the paragraph still has any
// content left (false means the marker was alone and the paragraph is now empty).
func stripMarker(para *ast.Paragraph) bool {
	txt := para.GetChildren()[0].(*ast.Text)
	lit := txt.Literal
	rest := lit[bytes.IndexByte(lit, ']')+1:]
	i := 0
	for i < len(rest) && (rest[i] == ' ' || rest[i] == '\t') {
		i++
	}
	if i >= len(rest) {
		para.SetChildren(para.GetChildren()[1:])
		return len(para.GetChildren()) > 0
	}
	txt.Literal = rest[i+1:] // drop the newline after the marker
	return true
}

// splitAlerts converts a blockquote into a sequence of sibling nodes, starting a
// new callout at every `[!TYPE]` marker. Splitting is needed because the parser
// (with NoEmptyLineBeforeBlock) merges blank-line-separated blockquotes into one,
// which would otherwise collapse stacked alerts into a single box. Returns nil
// when the blockquote holds no alert markers, leaving it untouched.
func splitAlerts(bq *ast.BlockQuote) []ast.Node {
	children := bq.GetChildren()
	markerAt := make([]bool, len(children))
	any := false
	for i, c := range children {
		if p, ok := c.(*ast.Paragraph); ok {
			if _, isAlert := alertMarker(p); isAlert {
				markerAt[i] = true
				any = true
			}
		}
	}
	if !any {
		return nil
	}

	var out []ast.Node
	reparent := func(n ast.Node, seg []ast.Node) {
		n.SetChildren(seg)
		for _, c := range seg {
			c.SetParent(n)
		}
		out = append(out, n)
	}

	// Any content before the first marker stays a plain blockquote.
	first := 0
	for first < len(children) && !markerAt[first] {
		first++
	}
	if first > 0 {
		reparent(&ast.BlockQuote{}, children[:first])
	}

	for i := first; i < len(children); {
		para := children[i].(*ast.Paragraph)
		kind, _ := alertMarker(para)
		j := i + 1
		for j < len(children) && !markerAt[j] {
			j++
		}
		seg := children[i:j]
		if !stripMarker(para) {
			seg = seg[1:] // marker was alone on its line; drop the empty paragraph
		}
		reparent(&AlertBlock{Kind: kind}, seg)
		i = j
	}
	return out
}

// transformAlerts rewrites alert blockquotes in the parsed document into
// AlertBlock nodes that the render hook turns into callout markup.
func transformAlerts(doc ast.Node) {
	var targets []*ast.BlockQuote
	ast.WalkFunc(doc, func(n ast.Node, entering bool) ast.WalkStatus {
		if entering {
			if bq, ok := n.(*ast.BlockQuote); ok {
				targets = append(targets, bq)
			}
		}
		return ast.GoToNext
	})
	for _, bq := range targets {
		replacements := splitAlerts(bq)
		if replacements == nil {
			continue
		}
		parent := bq.GetParent()
		if parent == nil {
			continue
		}
		kids := parent.GetChildren()
		idx := -1
		for i, c := range kids {
			if c == bq {
				idx = i
				break
			}
		}
		if idx < 0 {
			continue
		}
		newKids := make([]ast.Node, 0, len(kids)+len(replacements)-1)
		newKids = append(newKids, kids[:idx]...)
		newKids = append(newKids, replacements...)
		newKids = append(newKids, kids[idx+1:]...)
		for _, c := range replacements {
			c.SetParent(parent)
		}
		parent.SetChildren(newKids)
	}
}

// renderAlert is the renderer hook that emits GitHub-style callout markup for
// AlertBlock nodes; all other nodes fall through to the default renderer.
func renderAlert(w io.Writer, node ast.Node, entering bool) (ast.WalkStatus, bool) {
	a, ok := node.(*AlertBlock)
	if !ok {
		return ast.GoToNext, false
	}
	if entering {
		fmt.Fprintf(w, "<div class=\"markdown-alert markdown-alert-%s\">\n", a.Kind.slug)
		fmt.Fprintf(w, "<p class=\"markdown-alert-title\">%s%s</p>\n", a.Kind.icon, a.Kind.title)
	} else {
		io.WriteString(w, "</div>\n")
	}
	return ast.GoToNext, true
}

func expandWikiLinks(input string) string {
	return wikiLinkRe.ReplaceAllStringFunc(input, func(match string) string {
		parts := wikiLinkRe.FindStringSubmatch(match)
		if len(parts) != 2 {
			return match
		}
		raw := strings.TrimSpace(parts[1])
		if raw == "" {
			return match
		}

		target := raw
		label := raw
		if sep := strings.Index(raw, "|"); sep >= 0 {
			target = strings.TrimSpace(raw[:sep])
			label = strings.TrimSpace(raw[sep+1:])
			if target == "" || label == "" {
				return match
			}
		}

		pathParts := strings.Split(target, "/")
		encodedParts := make([]string, 0, len(pathParts))
		for _, part := range pathParts {
			trimmed := strings.TrimSpace(part)
			if trimmed == "" {
				return match
			}
			encodedParts = append(encodedParts, url.PathEscape(trimmed))
		}

		wikiPath := "/wiki/" + strings.Join(encodedParts, "/")
		return fmt.Sprintf("[%s](%s)", label, wikiPath)
	})
}

func main() {
	staticFS := http.FileServer(http.Dir("./static"))
	http.Handle("/static/", http.StripPrefix("/static/", staticFS))

	// API endpoints used by the React frontend.
	http.HandleFunc("/api/navigation", HandleNavigationAPI)
	http.HandleFunc("/api/home", HandleHomeAPI)
	http.HandleFunc("/api/wiki", HandleWikiAPI)
	http.HandleFunc("/api/search", HandleSearchAPI)
	http.HandleFunc("/api/search/suggest", HandleSearchSuggest)

	// Keep legacy suggestion endpoint for backward compatibility.
	http.HandleFunc("/search/suggest", HandleSearchSuggest)

	// Serve the SPA for all non-API routes.
	http.HandleFunc("/", HandleAppShell)

	addr := envOrDefault("MINIWIKI_ADDR", DefaultAddr)
	certPath := envOrDefault("MINIWIKI_TLS_CERT", path.Join(DefaultCertDir, DefaultCert))
	keyPath := envOrDefault("MINIWIKI_TLS_KEY", path.Join(DefaultCertDir, DefaultKey))

	if err := ensureSelfSignedTLSCert(certPath, keyPath); err != nil {
		log.Fatalf("failed to prepare TLS certificate: %v", err)
	}

	log.Printf("miniwiki listening on https://localhost%s", addr)
	log.Fatal(http.ListenAndServeTLS(addr, certPath, keyPath, nil))
}

func envOrDefault(key, fallback string) string {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	return v
}

func ensureSelfSignedTLSCert(certPath, keyPath string) error {
	if _, certErr := os.Stat(certPath); certErr == nil {
		if _, keyErr := os.Stat(keyPath); keyErr == nil {
			return nil
		}
	}

	if err := os.MkdirAll(path.Dir(certPath), 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(path.Dir(keyPath), 0o755); err != nil {
		return err
	}

	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return err
	}

	serialLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serialNumber, err := rand.Int(rand.Reader, serialLimit)
	if err != nil {
		return err
	}

	certTemplate := &x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			Organization: []string{"Miniwiki"},
			CommonName:   "localhost",
		},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().AddDate(1, 0, 0),
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{"localhost"},
	}

	ipv4, err := netip.ParseAddr("127.0.0.1")
	if err != nil {
		return err
	}
	ipv6, err := netip.ParseAddr("::1")
	if err != nil {
		return err
	}
	certTemplate.IPAddresses = append(certTemplate.IPAddresses, ipv4.AsSlice(), ipv6.AsSlice())

	derBytes, err := x509.CreateCertificate(rand.Reader, certTemplate, certTemplate, &privateKey.PublicKey, privateKey)
	if err != nil {
		return err
	}

	certOut, err := os.Create(certPath)
	if err != nil {
		return err
	}
	defer certOut.Close()
	if err := pem.Encode(certOut, &pem.Block{Type: "CERTIFICATE", Bytes: derBytes}); err != nil {
		return err
	}

	keyOut, err := os.OpenFile(keyPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer keyOut.Close()
	keyBytes := x509.MarshalPKCS1PrivateKey(privateKey)
	if err := pem.Encode(keyOut, &pem.Block{Type: "RSA PRIVATE KEY", Bytes: keyBytes}); err != nil {
		return err
	}

	return nil
}

func HandleAppShell(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.NotFound(w, r)
		return
	}
	requestPath := path.Clean(strings.TrimPrefix(r.URL.Path, "/"))
	if requestPath == "." {
		requestPath = ""
	}
	distRoot := path.Join(cwd, "frontend", "dist")
	if requestPath != "" {
		filePath := path.Join(distRoot, requestPath)
		if info, err := os.Stat(filePath); err == nil && !info.IsDir() {
			http.ServeFile(w, r, filePath)
			return
		}
	}
	indexPath := path.Join(distRoot, "index.html")
	if _, err := os.Stat(indexPath); err != nil {
		http.Error(w, "frontend build not found, run frontend build first", http.StatusServiceUnavailable)
		return
	}
	http.ServeFile(w, r, indexPath)
}

func listRootWikiEntries() ([]NavigationElement, []NavigationElement, error) {
	wikiContent, err := os.ReadDir("./wiki")
	if err != nil {
		return nil, nil, err
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
			continue
		}
		if strings.HasSuffix(title, ".md") {
			title = title[:len(title)-3]
			if strings.EqualFold(title, "README") {
				continue
			}
			articles = append(articles, NavigationElement{
				Title: title,
				Link:  fmt.Sprintf("/wiki/%s", title),
			})
		}
	}
	sort.Slice(categories, func(i, j int) bool { return categories[i].Title < categories[j].Title })
	sort.Slice(articles, func(i, j int) bool { return articles[i].Title < articles[j].Title })
	return categories, articles, nil
}

func sanitizeWikiRelPath(raw string) (string, error) {
	trimmed := strings.TrimSpace(strings.TrimPrefix(raw, "/"))
	if trimmed == "" {
		return "", nil
	}
	clean := path.Clean(trimmed)
	if clean == "." {
		return "", nil
	}
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return "", errors.New("invalid wiki path")
	}
	return clean, nil
}

func loadArticleByRelPath(relPath string) (string, string, error) {
	absPath := path.Join(cwd, "wiki", relPath)
	absPath += ".md"
	f, err := os.ReadFile(absPath)
	if err != nil {
		return "", "", err
	}
	title := path.Base(relPath)
	md := applyDynamicVars(string(f))
	rendered := string(ParseMarkdown([]byte(md)))
	return title, rendered, nil
}

func loadDirectoryByRelPath(relPath string) (string, []NavigationElement, []NavigationElement, error) {
	absPath := path.Join(cwd, "wiki", relPath)
	d, err := os.ReadDir(absPath)
	if err != nil {
		return "", nil, nil, err
	}
	articles := make([]NavigationElement, 0)
	topics := make([]NavigationElement, 0)
	for _, el := range d {
		if el.IsDir() {
			topics = append(topics, NavigationElement{
				Title: el.Name(),
				Link:  path.Join("/", "wiki", relPath, el.Name()),
			})
			continue
		}
		if !strings.HasSuffix(el.Name(), ".md") {
			continue
		}
		name := strings.TrimSuffix(el.Name(), ".md")
		articles = append(articles, NavigationElement{
			Title: name,
			Link:  path.Join("/", "wiki", relPath, name),
		})
	}
	sort.Slice(articles, func(i, j int) bool { return articles[i].Title < articles[j].Title })
	sort.Slice(topics, func(i, j int) bool { return topics[i].Title < topics[j].Title })
	title := path.Base(relPath)
	return title, articles, topics, nil
}

func convertNavigation(nav map[NavigationElement][]NavigationElement) []APINavigationSection {
	sections := make([]APINavigationSection, 0, len(nav))
	for key, items := range nav {
		copiedItems := make([]NavigationElement, len(items))
		copy(copiedItems, items)
		sort.Slice(copiedItems, func(i, j int) bool { return copiedItems[i].Title < copiedItems[j].Title })
		sections = append(sections, APINavigationSection{
			Title: key.Title,
			Link:  key.Link,
			Items: copiedItems,
		})
	}
	sort.Slice(sections, func(i, j int) bool { return sections[i].Title < sections[j].Title })
	return sections
}

func HandleNavigationAPI(w http.ResponseWriter, _ *http.Request) {
	nav, err := GenerateSidebarContents()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(convertNavigation(nav))
}

func HandleHomeAPI(w http.ResponseWriter, _ *http.Request) {
	categories, articles, err := listRootWikiEntries()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	landingTitle, landingContent, landingErr := loadArticleByRelPath("README")
	if landingErr != nil {
		landingTitle = "MiniWiki"
		landingContent = "<p>Welcome to MiniWiki.</p>"
	}
	response := APIHomeResponse{
		Categories: categories,
		Articles:   articles,
	}
	response.Landing.Title = landingTitle
	response.Landing.ContentHTML = landingContent
	response.Landing.Link = "/"
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(response)
}

func HandleWikiAPI(w http.ResponseWriter, r *http.Request) {
	relPath, err := sanitizeWikiRelPath(r.URL.Query().Get("path"))
	if err != nil || relPath == "" {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}
	absPath := path.Join(cwd, "wiki", relPath)
	fi, statErr := os.Stat(absPath)
	if statErr == nil && fi.IsDir() {
		title, articles, topics, dirErr := loadDirectoryByRelPath(relPath)
		if dirErr != nil {
			http.Error(w, dirErr.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(APIWikiResponse{
			Mode:     "directory",
			Title:    title,
			RelPath:  relPath,
			Articles: articles,
			Topics:   topics,
		})
		return
	}
	title, content, articleErr := loadArticleByRelPath(relPath)
	if articleErr != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(APIWikiResponse{
		Mode:        "article",
		Title:       title,
		RelPath:     relPath,
		Content:     content,
		ContentHTML: content,
	})
}

func HandleSearchAPI(w http.ResponseWriter, r *http.Request) {
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if err := ensureSearchIndexFresh(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	results, indexedAt := searchDocs(query)
	out := make([]APISearchResult, 0, len(results))
	for _, result := range results {
		out = append(out, APISearchResult{
			Title:            result.Title,
			Link:             result.Link,
			Path:             result.Path,
			RenderedSnippet:  string(result.RenderedSnippet),
			PlainSnippet:     result.PlainSnippet,
			HighlightedPlain: string(result.HighlightedSnippet),
		})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(struct {
		Query      string            `json:"query"`
		TotalCount int               `json:"total_count"`
		IndexedAt  string            `json:"indexed_at"`
		Results    []APISearchResult `json:"results"`
	}{
		Query:      query,
		TotalCount: len(out),
		IndexedAt:  indexedAt.Format(time.RFC3339),
		Results:    out,
	})
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
	nav, err := GenerateSidebarContents()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	err = templates.ExecuteTemplate(w, "layout.tmpl", Data{
		Title:      "MiniWiki",
		Mode:       "home",
		Navigation: nav,
		Directory: struct {
			Articles []NavigationElement
			Topics   []NavigationElement
		}{
			Articles: articles,
			Topics:   categories,
		},
	})
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
	text := markdownToRenderedPlainText(content)
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
	trimmedQuery := strings.TrimSpace(query)
	if trimmedQuery == "" {
		rendered := string(ParseMarkdown([]byte(content)))
		return template.HTML(rendered)
	}
	if strings.TrimSpace(content) == "" {
		return template.HTML("")
	}
	fragment, hasHead, hasTail := extractMarkdownContextBlock(content, trimmedQuery)
	if strings.TrimSpace(fragment) == "" {
		return template.HTML("")
	}
	if isHeavyRenderedSnippet(fragment) {
		return template.HTML("")
	}
	if hasHead {
		fragment = "...\n\n" + fragment
	}
	if hasTail {
		fragment += "\n\n..."
	}

	rendered := string(ParseMarkdown([]byte(fragment)))
	return highlightRenderedHTMLText(rendered, trimmedQuery)
}

func isHeavyRenderedSnippet(fragment string) bool {
	return heavySnippetTableRe.MatchString(fragment) ||
		heavySnippetFenceRe.MatchString(fragment) ||
		heavySnippetHTMLRe.MatchString(fragment)
}

func extractMarkdownContextBlock(content, query string) (string, bool, bool) {
	lines := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	if len(lines) == 0 {
		return "", false, false
	}
	lowerQuery := strings.ToLower(strings.TrimSpace(query))
	matchLine := -1
	for i, line := range lines {
		if strings.Contains(strings.ToLower(line), lowerQuery) {
			matchLine = i
			break
		}
	}
	if matchLine < 0 {
		return "", false, false
	}

	start := matchLine
	for start > 0 && strings.TrimSpace(lines[start-1]) != "" {
		start--
	}
	end := matchLine
	for end+1 < len(lines) && strings.TrimSpace(lines[end+1]) != "" {
		end++
	}
	fragment := strings.TrimSpace(strings.Join(lines[start:end+1], "\n"))
	return fragment, start > 0, end < len(lines)-1
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
		var renderedSnippet template.HTML
		plainSnippet := ""
		highlightedSnippet := template.HTML("")
		if contentHit {
			renderedSnippet = makeRenderedSearchSnippet(doc.Content, query)
			plainSnippet = makeSearchSnippet(doc.Content, query)
			highlightedSnippet = highlightSearchPhrase(plainSnippet, query)
		}
		results = append(results, SearchResult{
			Title:              doc.Title,
			Link:               doc.Link,
			Path:               doc.Path,
			RenderedSnippet:    renderedSnippet,
			PlainSnippet:       plainSnippet,
			HighlightedSnippet: highlightedSnippet,
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
		if contentHas {
			score += 10
		}
		matches = append(matches, scoredSuggestion{
			SearchSuggestion: SearchSuggestion{
				Title:    doc.Title,
				Link:     doc.Link,
				Path:     doc.Path,
				Category: suggestionCategory(doc.Path),
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

func suggestionCategory(docPath string) string {
	parts := strings.Split(strings.TrimSpace(docPath), "/")
	if len(parts) >= 2 && parts[0] != "" {
		return parts[0]
	}
	return "other"
}

func markdownToRenderedPlainText(content string) string {
	if content == "" {
		return ""
	}
	rendered := string(ParseMarkdown([]byte(content)))
	plain := renderedBlockTagRe.ReplaceAllString(rendered, " ")
	plain = renderedAnyTagRe.ReplaceAllString(plain, " ")
	plain = stdhtml.UnescapeString(plain)
	return strings.Join(strings.Fields(plain), " ")
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
	result := make(map[NavigationElement][]NavigationElement)
	d, err := os.ReadDir(path.Join(cwd, "wiki"))
	if err != nil {
		return nil, errors.New(err.Error())
	}
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
			if !strings.HasSuffix(title, ".md") {
				continue
			}
			// Root markdown files are intentionally hidden from sidebar navigation.
			// The landing markdown is shown on "/" and accessible via the top-left brand link.
			continue
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
