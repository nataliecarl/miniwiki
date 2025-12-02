package main

import (
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"os"
	"path"
	"regexp"
	"strings"
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
		link := fmt.Sprintf("/wiki/%s", title)
		if x.IsDir() {
			categories = append(categories, NavigationElement{
				Title: title,
				Link:  link,
			})
		} else {
			if title[:len(title)-3] == ".md" {
				title = title[:len(title)-3]
				articles = append(articles, NavigationElement{
					Title: title,
					Link:  link,
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
	Navigation map[NavigationElement][]NavigationElement
	Content    template.HTML
	Directory  struct {
		Articles []NavigationElement
		Topics   []NavigationElement
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
	months := map[int]string{1: "January", 2: "February", 3: "March", 4: "April", 5: "May", 6: "June", 7: "July", 8: "August", 9: "September", 10: "October", 11: "November", 12: "December",}
	if monthStr, exists := months[month]; exists {
		return monthStr
	}
	return ""
}

func monthStringToInt(monthStr string) (int, error) {
	months := map[string]int{"jan": 1,"feb": 2, "mar": 3, "apr": 4, "may": 5, "jun": 6, "jul": 7, "aug": 8, "sep": 9, "oct": 10, "nov": 11, "dec": 12,}
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
