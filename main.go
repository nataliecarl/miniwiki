package main

import (
	"html/template"
	"log"
	"net/http"
	"os"
	"path"
	"regexp"

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
	RendererFlags = html.CommonFlags // https://pkg.go.dev/github.com/gomarkdown/markdown@v0.0.0-20250810172220-2e2c11897d1a/html#Flags
)

var (
	htmlRenderer *html.Renderer
	templates *template.Template
	cwd string
)

func init() {
	htmlRenderer = html.NewRenderer(html.RendererOptions{
		Flags: RendererFlags,
	})
	cwd, err := os.Getwd()
	if err != nil {
		panic("unable to get cwd " + err.Error())
	}
	templates = template.Must(template.ParseFiles(
		path.Join(cwd, "templates", "article.tmpl"), // TODO add more templates here
	))
}

func ParseMarkdown(document []byte) []byte {
	p := parser.NewWithExtensions(ParserFlags)
	d := p.Parse(document)
	return markdown.Render(d, htmlRenderer)
}

func main() {
	
	// TODO load all templates here



	/*
			tpl, err := template.ParseFiles("./templates/article.tmpl")
			Must(err)

			html := string(ParseMarkdown([]byte(`
		# hello world

		> this is a quote

		and a list:
		- hi
		- another one
		- test

		`)))
			err = tpl.Execute(os.Stdout, struct {
				Content template.HTML
			}{template.HTML(html)})
			Must(err)
	*/

	http.HandleFunc("/", handleIndex)
	http.HandleFunc("/wiki/", MakeHandler(HandleView))
	fs := http.FileServer(http.Dir("./static"))
	http.Handle("/static/", http.StripPrefix("/static/", fs))
	http.ListenAndServe(":8080", nil)

}

func handleIndex(w http.ResponseWriter, r *http.Request) {
	println("unimplemented")
}

// article | is path if nested, else just the document title (without file extension)
func HandleView(w http.ResponseWriter, r *http.Request, article string) {

	log := func(msg string) {
		log.Printf("HandleView: %s", msg)
	}

	// TODO implement additional directory view to show subset of articles

	// check if such a wiki entry exists
	cwd, err := os.Getwd()
	if err != nil {
		panic("something's wrong here, what is this running on?")
	}
	articlePath := path.Join(cwd, "wiki", article+".md")
	if _, err := os.Stat(articlePath); err != nil {
		log(err.Error())
	} else {
		log("file exists " + articlePath)
	}

	// use template to create html
	content, err := os.ReadFile(articlePath)
	if err != nil {
		log("file not found although should exist " + err.Error())
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	contentHtml := string(ParseMarkdown(content))
	RenderArticle(w, "article.tmpl", contentHtml)

	// return that with 200 ok
	//w.WriteHeader(http.StatusOK)
}

func RenderArticle(w http.ResponseWriter, tmpl string, content string) {
	err := templates.ExecuteTemplate(w, tmpl, struct {
	//err := templates.ExecuteTemplate(os.Stdout, tmpl, struct {
		Content template.HTML
	}{template.HTML(content)})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// var validPath = regexp.MustCompile("^/(edit|save|view)/([a-zA-Z0-9]+)$")
var validPath = regexp.MustCompile("^/wiki/([a-zA-Z0-9/]+)$")

// source: https://go.dev/doc/articles/wiki/
func MakeHandler(fn func(http.ResponseWriter, *http.Request, string)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// TODO log for all requests here
		m := validPath.FindStringSubmatch(r.URL.Path)
		if m == nil {
			// TODO uncomment
			http.NotFound(w, r)
			return
		}
		fn(w, r, m[1])
	}
}
