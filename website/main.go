// Command website builds the static site from the repository's markdown.
//
//	go run . [-root ..] [-out ../_site] [-serve localhost:8000]
//
// GitHub's markdown API renders each document exactly as github.com shows
// it, with HTML sanitised and GitHub's own heading slugs. Set GITHUB_TOKEN to
// authenticate; without it the API allows 60 requests an hour.
package main

import (
	"bytes"
	"embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"html"
	"html/template"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const repo = "https://github.com/SEAN-7/bot"

//go:embed style.css favicon.svg
var static embed.FS

type page struct{ src, out, name string }

// pages are rendered under docs/; links between them are rewritten, and
// other relative links point at the file on GitHub.
var pages = []page{
	{"README.md", "docs", "Guide"},
	{"bot/docs/philosophy.md", "docs/philosophy", "Philosophy"},
	{"bot/docs/spec.md", "docs/spec", "Spec"},
	{"bot/internal/notes/notes.md", "docs/changelog", "Release notes"},
}

func main() {
	root := flag.String("root", "..", "repository root")
	out := flag.String("out", "../_site", "output directory (replaced)")
	serve := flag.String("serve", "", "after building, serve the site at this address")
	flag.Parse()
	b := &builder{root: *root, out: *out, render: githubRender}
	if err := b.build(); err != nil {
		fmt.Fprintln(os.Stderr, "website:", err)
		os.Exit(1)
	}
	fmt.Println("built", *out)
	if *serve != "" {
		fmt.Printf("serving http://%s/\n", *serve)
		if err := http.ListenAndServe(*serve, http.FileServer(http.Dir(*out))); err != nil {
			fmt.Fprintln(os.Stderr, "website:", err)
			os.Exit(1)
		}
	}
}

// githubRender renders markdown as GitHub renders a README.
func githubRender(markdown string) (string, error) {
	body, _ := json.Marshal(map[string]string{"text": markdown, "mode": "markdown"})
	req, err := http.NewRequest("POST", "https://api.github.com/markdown", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if t := os.Getenv("GITHUB_TOKEN"); t != "" {
		req.Header.Set("Authorization", "Bearer "+t)
	}
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("markdown API: %s: %s", resp.Status, bytes.TrimSpace(out))
	}
	return string(out), nil
}

type builder struct {
	root, out string
	render    func(markdown string) (string, error)
	assets    map[string]bool // repository paths of images to copy
}

type heading struct{ ID, Text string }

type doc struct {
	page
	title string
	body  template.HTML
	heads []heading // h2s, for the side navigation
}

func (b *builder) build() error {
	b.assets = map[string]bool{}
	notes, err := b.read("bot/internal/notes/notes.md")
	if err != nil {
		return err
	}
	version := ""
	if m := regexp.MustCompile(`(?m)^## (\d+\.\d+\.\d+)`).FindStringSubmatch(notes); m != nil {
		version = m[1]
	}

	var docs []doc
	for _, p := range pages {
		md, err := b.read(p.src)
		if err != nil {
			return err
		}
		body, heads, err := b.convert(md, p.src, p.out)
		if err != nil {
			return fmt.Errorf("%s: %w", p.src, err)
		}
		title := p.name
		if m := regexp.MustCompile(`(?s)<h1[^>]*>(.*?)</h1>`).FindStringSubmatch(body); m != nil {
			title = text(m[1])
		}
		docs = append(docs, doc{p, title, template.HTML(body), heads})
	}
	home, err := b.home()
	if err != nil {
		return err
	}

	if err := os.RemoveAll(b.out); err != nil {
		return err
	}
	for _, name := range []string{"style.css", "favicon.svg"} {
		data, _ := static.ReadFile(name)
		if err := write(filepath.Join(b.out, name), data); err != nil {
			return err
		}
	}
	for a := range b.assets {
		data, err := os.ReadFile(filepath.Join(b.root, a))
		if err != nil {
			return err
		}
		if err := write(filepath.Join(b.out, "assets", a), data); err != nil {
			return err
		}
	}

	if err := b.page(".", layout{Title: "bot — an agent with one tool", Class: "home", Main: home}, version); err != nil {
		return err
	}
	for _, d := range docs {
		var nav []navItem
		for _, o := range docs {
			item := navItem{Name: o.name, Href: rel(d.out, o.out)}
			if o.out == d.out {
				item.Here, item.Heads = true, d.heads
			}
			nav = append(nav, item)
		}
		var buf bytes.Buffer
		if err := tmpl.ExecuteTemplate(&buf, "doc", struct {
			Body template.HTML
			Src  string
			Repo string
		}{d.body, d.src, repo}); err != nil {
			return err
		}
		l := layout{Title: d.title + " — bot", Class: "doc", Nav: nav, Main: template.HTML(buf.String())}
		if err := b.page(d.out, l, version); err != nil {
			return err
		}
	}
	return nil
}

// home renders the landing page from the README's introduction and the
// philosophy's three ideas.
func (b *builder) home() (template.HTML, error) {
	readme, err := b.read("README.md")
	if err != nil {
		return "", err
	}
	phil, err := b.read("bot/docs/philosophy.md")
	if err != nil {
		return "", err
	}
	intro, err := paragraph(readme, 1)
	if err != nil {
		return "", fmt.Errorf("README.md: %w", err)
	}
	lede, err := paragraph(phil, 1)
	if err != nil {
		return "", fmt.Errorf("bot/docs/philosophy.md: %w", err)
	}
	ideas, err := section(phil, "Three ideas")
	if err != nil {
		return "", fmt.Errorf("bot/docs/philosophy.md: %w", err)
	}
	introHTML, _, err := b.convert(intro, "README.md", ".")
	if err != nil {
		return "", err
	}
	ideasHTML, _, err := b.convert(ideas, "bot/docs/philosophy.md", ".")
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	err = tmpl.ExecuteTemplate(&buf, "home", struct {
		Intro, Ideas template.HTML
		Lede         string
	}{template.HTML(introHTML), template.HTML(ideasHTML), text(lede)})
	return template.HTML(buf.String()), err
}

var (
	headingRE    = regexp.MustCompile(`(?s)<div class="markdown-heading"><h([1-6]) class="heading-element">(.*?)</h[1-6]><a id="user-content-([^"]*)"[^>]*>.*?</a></div>`)
	anyHeadingRE = regexp.MustCompile(`<h[1-6](?:\s[^>]*)?>`)
	idHeadingRE  = regexp.MustCompile(`^<h[1-6] id="[^"]*">$`)
	attrRE       = regexp.MustCompile(`(<(?:a|img)\s[^>]*?\b(?:href|src)=")([^"]*)(")`)
	schemeRE     = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9+.-]*:`)
)

// convert renders markdown from the repository file src for the page at
// output directory outdir: headings get GitHub's slugs as ids, links to
// other pages become site links, and relative images are copied.
func (b *builder) convert(markdown, src, outdir string) (string, []heading, error) {
	s, err := b.render(markdown)
	if err != nil {
		return "", nil, err
	}
	var heads []heading
	s = headingRE.ReplaceAllStringFunc(s, func(m string) string {
		g := headingRE.FindStringSubmatch(m)
		if g[1] == "2" {
			heads = append(heads, heading{html.UnescapeString(g[3]), text(g[2])})
		}
		return fmt.Sprintf(`<h%s id="%s">%s</h%s>`, g[1], g[3], g[2], g[1])
	})
	// Every heading must have been rewritten, or its anchors would break.
	for _, h := range anyHeadingRE.FindAllString(s, -1) {
		if !idHeadingRE.MatchString(h) {
			return "", nil, errors.New("unexpected heading markup from the markdown API: " + h)
		}
	}
	s = attrRE.ReplaceAllStringFunc(s, func(m string) string {
		g := attrRE.FindStringSubmatch(m)
		u := html.UnescapeString(g[2])
		if strings.HasPrefix(g[1], "<img") {
			u = b.image(src, outdir, u)
		} else {
			u = link(src, outdir, u)
		}
		return g[1] + html.EscapeString(u) + g[3]
	})
	return s, heads, nil
}

// target resolves a relative URL in the repository file src to a
// repository path, or returns "" for anything else.
func target(src, u string) (p, frag string) {
	if u == "" || strings.HasPrefix(u, "#") || strings.HasPrefix(u, "/") || schemeRE.MatchString(u) {
		return "", ""
	}
	u, frag, _ = strings.Cut(u, "#")
	p = path.Clean(path.Join(path.Dir(src), u))
	if p == ".." || strings.HasPrefix(p, "../") {
		return "", ""
	}
	if frag != "" {
		frag = "#" + frag
	}
	return p, frag
}

func link(src, outdir, u string) string {
	p, frag := target(src, u)
	if p == "" {
		return u
	}
	for _, o := range pages {
		if o.src == p {
			return rel(outdir, o.out) + frag
		}
	}
	return repo + "/blob/main/" + p + frag
}

func (b *builder) image(src, outdir, u string) string {
	p, _ := target(src, u)
	if p == "" {
		return u
	}
	b.assets[p] = true
	return rel(outdir, path.Dir(path.Join("assets", p))) + path.Base(p)
}

// rel is the relative URL from output directory from to directory to.
func rel(from, to string) string {
	r, _ := filepath.Rel(filepath.FromSlash(from), filepath.FromSlash(to))
	if r == "." {
		return "./"
	}
	return filepath.ToSlash(r) + "/"
}

// paragraph returns the nth blank-line-separated block of markdown.
func paragraph(md string, n int) (string, error) {
	ps := strings.Split(strings.TrimSpace(md), "\n\n")
	if n >= len(ps) {
		return "", fmt.Errorf("no paragraph %d", n)
	}
	return strings.TrimSpace(ps[n]), nil
}

// section returns the body of the "## name" section of md.
func section(md, name string) (string, error) {
	_, rest, ok := strings.Cut(md, "\n## "+name+"\n")
	if !ok {
		return "", fmt.Errorf("no %q section", "## "+name)
	}
	if i := strings.Index(rest, "\n## "); i >= 0 {
		rest = rest[:i]
	}
	return strings.TrimSpace(rest), nil
}

// text is the plain text of an HTML fragment.
func text(s string) string {
	return strings.TrimSpace(html.UnescapeString(regexp.MustCompile(`<[^>]+>`).ReplaceAllString(s, "")))
}

func (b *builder) read(p string) (string, error) {
	data, err := os.ReadFile(filepath.Join(b.root, filepath.FromSlash(p)))
	return string(data), err
}

func write(name string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		return err
	}
	return os.WriteFile(name, data, 0o644)
}

type navItem struct {
	Name, Href string
	Here       bool
	Heads      []heading
}

type layout struct {
	Title, Class, Up, Repo, Version string
	Nav                             []navItem
	Main                            template.HTML
}

func (b *builder) page(outdir string, l layout, version string) error {
	l.Up, l.Repo, l.Version = rel(outdir, "."), repo, version
	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, "layout", l); err != nil {
		return err
	}
	return write(filepath.Join(b.out, filepath.FromSlash(outdir), "index.html"), buf.Bytes())
}

var tmpl = template.Must(template.New("").Parse(`
{{- define "layout" -}}
<!doctype html>
<html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>{{.Title}}</title>
<meta name="description" content="A minimal, always-on LLM agent. One tool: the shell.">
<link rel="stylesheet" href="{{.Up}}style.css">
<link rel="icon" href="{{.Up}}favicon.svg">
</head><body class="{{.Class}}">
<header class="top"><a class="mark" href="{{.Up}}">bot<span class="cur"></span></a>
<nav><a href="{{.Up}}docs/">docs</a><a href="{{.Repo}}">github</a></nav></header>
{{with .Nav}}<aside class="side"><ul>
{{- range .}}<li{{if .Here}} class="here"{{end}}><a href="{{.Href}}">{{.Name}}</a>
{{- with .Heads}}<ul>{{range .}}<li><a href="#{{.ID}}">{{.Text}}</a></li>{{end}}</ul>{{end}}</li>{{end -}}
</ul></aside>{{end}}<main>{{.Main}}</main>
<footer>{{with .Version}}v{{.}} · {{end}}built from <a href="{{.Repo}}">SEAN-7/bot</a></footer>
</body></html>
{{end}}

{{- define "doc" -}}
{{.Body}}<p class="edit"><a href="{{.Repo}}/blob/main/{{.Src}}">{{.Src}}</a></p>
{{- end}}

{{- define "home" -}}
<section class="hero">
<h1>An always-on agent<br>with one tool: <em>the shell</em>.</h1>
<div class="lede">{{.Intro}}</div>
<pre class="term"><span class="p">$</span> sudo agent send main "Read your README, then tell me how you plan to keep notes."
<span class="p">$</span> sudo agent messages main</pre>
<p class="cta"><a href="docs/">Read the docs →</a></p>
</section>
<section class="ideas"><h2>{{.Lede}}</h2>{{.Ideas}}</section>
{{- end}}
`))
