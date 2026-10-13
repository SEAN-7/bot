package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// apiHeading is how the markdown API renders "### Héllo, `wörld` & co_x".
const apiHeading = `<div class="markdown-heading"><h3 class="heading-element">Héllo, <code>wörld</code> &amp; co_x</h3><a id="user-content-héllo-wörld--co_x" class="anchor" aria-label="Permalink: Héllo, wörld &amp; co_x" href="#héllo-wörld--co_x"><span aria-hidden="true" class="octicon octicon-link"></span></a></div>`

func fixed(out string) func(string) (string, error) {
	return func(string) (string, error) { return out, nil }
}

func TestConvertHeadings(t *testing.T) {
	b := &builder{render: fixed(apiHeading + "\n" + strings.Replace(apiHeading, "h3", "h2", 2)), assets: map[string]bool{}}
	got, heads, err := b.convert("", "README.md", "docs")
	if err != nil {
		t.Fatal(err)
	}
	want := `<h3 id="héllo-wörld--co_x">Héllo, <code>wörld</code> &amp; co_x</h3>`
	if !strings.HasPrefix(got, want) {
		t.Errorf("got %q, want prefix %q", got, want)
	}
	if len(heads) != 1 || heads[0] != (heading{"héllo-wörld--co_x", "Héllo, wörld & co_x"}) {
		t.Errorf("h2s = %q", heads)
	}
}

func TestConvertRejectsUnknownHeadings(t *testing.T) {
	b := &builder{render: fixed("<h2>Install</h2>"), assets: map[string]bool{}}
	if _, _, err := b.convert("", "README.md", "docs"); err == nil {
		t.Fatal("a heading without an id was accepted")
	}
}

func TestConvertLinks(t *testing.T) {
	cases := []struct{ src, outdir, href, want string }{
		{"README.md", "docs", "bot/docs/spec.md#install", "spec/#install"},
		{"README.md", ".", "bot/docs/spec.md", "docs/spec/"},
		{"bot/docs/spec.md", "docs/spec", "philosophy.md", "../philosophy/"},
		{"bot/docs/spec.md", "docs/spec", "../install.sh", repo + "/blob/main/bot/install.sh"},
		{"README.md", "docs", "#if-pooljson-is-lost", "#if-pooljson-is-lost"},
		{"README.md", "docs", "https://example.com/a?b=1&amp;c=2", "https://example.com/a?b=1&amp;c=2"},
		{"README.md", "docs", "../outside.md", "../outside.md"},
	}
	for _, c := range cases {
		b := &builder{render: fixed(`<p><a href="` + c.href + `">x</a></p>`), assets: map[string]bool{}}
		got, _, err := b.convert("", c.src, c.outdir)
		if err != nil {
			t.Fatal(err)
		}
		if want := `<p><a href="` + c.want + `">x</a></p>`; got != want {
			t.Errorf("%s in %s: got %s, want %s", c.href, c.src, got, want)
		}
	}
}

func TestConvertImages(t *testing.T) {
	b := &builder{render: fixed(`<img src="docs/arch.svg" alt="">`), assets: map[string]bool{}}
	got, _, err := b.convert("", "bot/README.md", "docs/spec")
	if err != nil {
		t.Fatal(err)
	}
	if want := `<img src="../../assets/bot/docs/arch.svg" alt="">`; got != want {
		t.Errorf("got %s, want %s", got, want)
	}
	if !b.assets["bot/docs/arch.svg"] {
		t.Errorf("assets = %v", b.assets)
	}
}

func TestSection(t *testing.T) {
	md := "# T\n\nLede.\n\n## Three ideas\n\n1. One\n2. Two\n\n## Next\n\nx"
	if got, err := section(md, "Three ideas"); err != nil || got != "1. One\n2. Two" {
		t.Errorf("section = %q, %v", got, err)
	}
	if got, err := section("# T\n\n## Three ideas\n\nlast", "Three ideas"); err != nil || got != "last" {
		t.Errorf("last section = %q, %v", got, err)
	}
	if _, err := section(md, "Four ideas"); err == nil {
		t.Error("a missing section was accepted")
	}
}

// TestBuild builds a whole site from a small repository with a renderer
// that wraps the markdown, and checks titles are escaped exactly once.
func TestBuild(t *testing.T) {
	root, out := t.TempDir(), filepath.Join(t.TempDir(), "site")
	files := map[string]string{
		"README.md":                   "# Agents & plugins\n\nIntro.",
		"bot/docs/philosophy.md":      "# Philosophy\n\nLede.\n\n## Three ideas\n\n1. One",
		"bot/docs/spec.md":            "# Spec",
		"bot/internal/notes/notes.md": "# Release notes\n\n## 1.2.3\n\n- x",
	}
	for name, s := range files {
		if err := write(filepath.Join(root, name), []byte(s)); err != nil {
			t.Fatal(err)
		}
	}
	render := func(md string) (string, error) {
		if title, ok := strings.CutPrefix(md, "# Agents & plugins"); ok {
			return `<div class="markdown-heading"><h1 class="heading-element">Agents &amp; plugins</h1><a id="user-content-agents--plugins" class="anchor" href="#agents--plugins"></a></div>` + title, nil
		}
		return "<p>" + md + "</p>", nil
	}
	b := &builder{root: root, out: out, render: render}
	if err := b.build(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"index.html", "style.css", "favicon.svg",
		"fonts/MartianMono.woff2", "fonts/InstrumentSans.woff2", "fonts/MartianMono-OFL.txt", "fonts/InstrumentSans-OFL.txt", "docs/index.html", "docs/spec/index.html", "docs/philosophy/index.html", "docs/changelog/index.html"} {
		if _, err := os.Stat(filepath.Join(out, name)); err != nil {
			t.Error(err)
		}
	}
	guide, _ := os.ReadFile(filepath.Join(out, "docs/index.html"))
	if !strings.Contains(string(guide), "<title>Agents &amp; plugins — bot</title>") {
		t.Errorf("guide title not escaped once:\n%s", guide)
	}
	if !strings.Contains(string(guide), "v1.2.3 · built from") {
		t.Error("guide footer lacks the version")
	}
	if g := string(guide); !strings.Contains(g, `<link rel="stylesheet" href="../style.css">`) || !strings.Contains(g, `class="doc"`) {
		t.Errorf("guide lacks the stylesheet or doc class:\n%s", g)
	}
	home, _ := os.ReadFile(filepath.Join(out, "index.html"))
	h := string(home)
	if !strings.Contains(h, `<span class="dim">an always-on agent with one tool:</span> the shell.`) {
		t.Errorf("home lacks the headline:\n%s", h)
	}
	if strings.Count(h, `<span class="cur"></span>`) < 2 {
		t.Error("home lacks the header and command cursors")
	}
	if !strings.Contains(h, ">source</a>") || strings.Contains(h, ">github</a>") {
		t.Error("header should link the repository as source")
	}
	for _, want := range []string{`<canvas class="swarm" aria-hidden="true">`, `id="mascot-forms"`, `"home": "listen"`, `<script src="mascot/mascot.js" defer>`} {
		if !strings.Contains(h, want) {
			t.Errorf("home lacks %s", want)
		}
	}
	if _, err := os.Stat(filepath.Join(out, "mascot/mascot.js")); err != nil {
		t.Error(err)
	}
	if g := string(guide); strings.Contains(g, "mascot.js") || strings.Contains(g, "mascot-forms") {
		t.Error("doc pages should not load the mascot")
	}
}

// TestForms checks the mascot's shared definition, which the web renderer
// and a later terminal version both read.
func TestForms(t *testing.T) {
	data, err := static.ReadFile("mascot/forms.json")
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Particles struct{ Small, Large int }
		Scatter   []float64
		Pointer   struct{ Reach float64 }
		Home      string
		Order     []string
		Ambient   struct{ Sequence []string }
		Forms     map[string]struct {
			Shape      struct{ Type string }
			K, Damping float64
		}
		Font struct{ Glyphs map[string][]string }
	}
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatal(err)
	}
	names := append([]string{f.Home}, f.Order...)
	for _, s := range f.Ambient.Sequence {
		name, _, _ := strings.Cut(s, ":")
		names = append(names, name)
	}
	for _, n := range names {
		if _, ok := f.Forms[n]; !ok {
			t.Errorf("form %q is used but not defined", n)
		}
	}
	shapes := map[string]bool{"sphere": true, "cloud": true, "cube": true, "bud": true, "word": true}
	for n, form := range f.Forms {
		if !shapes[form.Shape.Type] || form.K <= 0 || form.Damping <= 0 {
			t.Errorf("form %q: shape %q, k %v, damping %v", n, form.Shape.Type, form.K, form.Damping)
		}
	}
	for _, r := range "abcdefghijklmnopqrstuvwxyz.!? " {
		rows, ok := f.Font.Glyphs[string(r)]
		if !ok || len(rows) != 7 {
			t.Errorf("glyph %q: %d rows", r, len(rows))
			continue
		}
		for _, row := range rows {
			if len(row) != 5 || strings.Trim(row, ".#") != "" {
				t.Errorf("glyph %q: bad row %q", r, row)
			}
		}
	}
	if len(f.Scatter) != 3 || f.Scatter[0] <= 0 || f.Scatter[1] <= 0 || f.Scatter[2] <= 0 || f.Pointer.Reach <= 0 {
		t.Errorf("scatter %v, pointer reach %v", f.Scatter, f.Pointer.Reach)
	}
	if f.Particles.Small <= 0 || f.Particles.Small >= f.Particles.Large {
		t.Errorf("particles: small %d, large %d", f.Particles.Small, f.Particles.Large)
	}
}
