#!/usr/bin/env python3
"""Build the static site from the repo's markdown. Usage: build.py [OUT]"""
import html, os, re, shutil, sys
from markdown_it import MarkdownIt

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
OUT = os.path.abspath(sys.argv[1] if len(sys.argv) > 1 else os.path.join(ROOT, "_site"))
REPO = "https://github.com/SEAN-7/bot"

# (source, output dir, nav title)
PAGES = [
    ("README.md", "docs", "Guide"),
    ("docs/philosophy.md", "docs/philosophy", "Philosophy"),
    ("docs/spec.md", "docs/spec", "Spec"),
    ("internal/notes/notes.md", "docs/changelog", "Release notes"),
]
SRC2OUT = {s: o for s, o, _ in PAGES}

def read(p):
    with open(os.path.join(ROOT, p), encoding="utf-8") as f:
        return f.read()

def version():
    m = re.search(r"^## (\d+\.\d+\.\d+)", read("internal/notes/notes.md"), re.M)
    return m.group(1) if m else ""

def slug(text):
    s = re.sub(r"[^\w\- ]", "", text.strip().lower())
    return s.replace(" ", "-")

def rel(frm, to):
    """Relative URL from output dir frm to output dir to."""
    r = os.path.relpath(to, frm)
    return "./" if r == "." else r + "/"

def render(src, outdir):
    md = MarkdownIt("commonmark").enable("table")
    tokens = md.parse(read(src))
    heads, seen = [], {}
    for i, t in enumerate(tokens):
        if t.type == "heading_open":
            text = tokens[i + 1].content
            s = slug(re.sub(r"`", "", text))
            n = seen.get(s, 0); seen[s] = n + 1
            if n: s = f"{s}-{n}"
            t.attrSet("id", s)
            if t.tag == "h2":
                heads.append((s, re.sub(r"`", "", text)))
        for c in ([t] + list(t.children or [])):
            if c.type == "link_open":
                href = c.attrGet("href") or ""
                if re.match(r"^[a-z]+:|^#", href):
                    continue
                path, _, frag = href.partition("#")
                target = os.path.normpath(os.path.join(os.path.dirname(src), path))
                if target in SRC2OUT:
                    new = rel(outdir, SRC2OUT[target])
                else:
                    new = f"{REPO}/blob/main/{target}"
                c.attrSet("href", new + ("#" + frag if frag else ""))
    body = md.renderer.render(tokens, md.options, {})
    # first h1 becomes the page title
    m = re.search(r"<h1[^>]*>(.*?)</h1>", body)
    title = re.sub("<[^>]+>", "", m.group(1)) if m else ""
    return title, body, heads

def page(outdir, title, main, nav="", cls=""):
    up = rel(outdir, ".")
    return f"""<!doctype html>
<html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>{html.escape(title)}</title>
<meta name="description" content="A minimal, always-on LLM agent. One tool: the shell.">
<link rel="stylesheet" href="{up}style.css">
<link rel="icon" href="{up}favicon.svg">
</head><body class="{cls}">
<header class="top"><a class="mark" href="{up}">bot<span class="cur"></span></a>
<nav><a href="{up}docs/">docs</a><a href="{REPO}">github</a></nav></header>
{nav}<main>{main}</main>
<footer>v{VERSION} · built from <a href="{REPO}">SEAN-7/bot</a></footer>
</body></html>
"""

def write(outdir, text):
    d = os.path.join(OUT, outdir)
    os.makedirs(d, exist_ok=True)
    with open(os.path.join(d, "index.html"), "w", encoding="utf-8") as f:
        f.write(text)

def home():
    phil = read("docs/philosophy.md")
    lede = phil.split("\n\n")[1].strip()
    ideas = re.search(r"## Three ideas\n\n(.*?)\n\n##", phil, re.S).group(1)
    md = MarkdownIt("commonmark")
    readme = read("README.md")
    intro = readme.split("\n\n")[1]
    intro = re.sub(r"\s*See\s+\[the spec\].*", "", intro, flags=re.S)
    main = f"""
<section class="hero">
<h1>An always-on agent<br>with one tool: <em>the shell</em>.</h1>
<p class="lede">It builds everything else itself — memory, schedules, messaging, tools — as plain files on a Linux VM. The harness only guarantees privilege separation, journals, crash recovery and context resets.</p>
<pre class="term"><span class="p">$</span> sudo agent send main "Read your README, then tell me how you plan to keep notes."
<span class="p">$</span> sudo agent messages main
<span class="c">main: I'll keep plain-text notes in ~/notes/, indexed from my README …</span></pre>
<p class="cta"><a href="docs/">Read the docs →</a></p>
</section>
<section class="ideas"><h2>{html.escape(lede)}</h2>{md.render(ideas)}</section>
"""
    write(".", page(".", "bot — an agent with one tool", main, cls="home"))

def docs():
    rendered = [(s, o, n) + render(s, o) for s, o, n in PAGES]
    for src, outdir, name, title, body, heads in rendered:
        items = []
        for s2, o2, n2, *_ in rendered:
            here = o2 == outdir
            sub = ""
            if here and heads:
                sub = "<ul>" + "".join(f'<li><a href="#{a}">{html.escape(t)}</a></li>' for a, t in heads) + "</ul>"
            items.append(f'<li{" class=here" if here else ""}><a href="{rel(outdir, o2)}">{n2}</a>{sub}</li>')
        nav = f'<aside class="side"><ul>{"".join(items)}</ul></aside>'
        edit = f'<p class="edit"><a href="{REPO}/blob/main/{src}">{src}</a></p>'
        write(outdir, page(outdir, f"{title or name} — bot", body + edit, nav, "doc"))

VERSION = version()
shutil.rmtree(OUT, ignore_errors=True)
os.makedirs(OUT)
here = os.path.dirname(os.path.abspath(__file__))
for f in ("style.css", "favicon.svg"):
    shutil.copy(os.path.join(here, f), OUT)
home(); docs()
print("built", OUT)
