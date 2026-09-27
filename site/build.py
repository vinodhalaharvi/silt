#!/usr/bin/env python3
"""
Build the site: content/*.md into public/, plus sitemap.xml and robots.txt.

Standard library only, on purpose. A static site for a product like this
needs a few dozen pages at most, and a generator with dependencies is a
thing that breaks between the times you touch it.

    ./build.py            build into public/
    ./build.py --serve    build, then serve it on http://localhost:8000

Each page is Markdown with a small header block at the top:

    ---
    title: Modbus simulator for testing an HMI
    description: One sentence, 150 characters or so. This is what Google
      shows under the link, so write it for a human.
    kind: article          (page | article; article adds a date and schema)
    date: 2026-09-22
    ---

The header's title and description are the two things that matter most for
search, so the build refuses to write a page missing either.
"""

import json
import html
import re
import shutil
import sys
from datetime import date
from pathlib import Path

ROOT = Path(__file__).parent
OUT = ROOT / "public"
CFG = json.loads((ROOT / "config.json").read_text())


# --- a small Markdown subset -------------------------------------------------
# Headings, paragraphs, lists, fenced code, tables, links, inline code, bold.
# Enough for a landing page and a technical post, and small enough to read.

def inline(text):
    text = html.escape(text, quote=False)
    text = re.sub(r"`([^`]+)`", r"<code>\1</code>", text)
    text = re.sub(r"\*\*([^*]+)\*\*", r"<strong>\1</strong>", text)
    # [label](url){.class} for the buy button; plain links otherwise
    text = re.sub(r"\[([^\]]+)\]\(([^)]+)\)\{\.([a-z-]+)\}",
                  r'<a class="\3" href="\2">\1</a>', text)
    text = re.sub(r"\[([^\]]+)\]\(([^)]+)\)", r'<a href="\2">\1</a>', text)
    return text


def markdown(src):
    out, lines, i = [], src.split("\n"), 0

    while i < len(lines):
        line = lines[i]

        if line.startswith("```"):
            lang = line[3:].strip()
            i += 1
            code = []
            while i < len(lines) and not lines[i].startswith("```"):
                code.append(html.escape(lines[i]))
                i += 1
            cls = f' class="lang-{lang}"' if lang else ""
            out.append(f"<pre><code{cls}>" + "\n".join(code) + "</code></pre>")

        elif line.startswith("#"):
            level = len(line) - len(line.lstrip("#"))
            text = line[level:].strip()
            slug = re.sub(r"[^a-z0-9]+", "-", text.lower()).strip("-")
            out.append(f'<h{level} id="{slug}">{inline(text)}</h{level}>')

        elif line.startswith("|"):
            rows = []
            while i < len(lines) and lines[i].startswith("|"):
                rows.append([c.strip() for c in lines[i].strip("|").split("|")])
                i += 1
            i -= 1
            head, body = rows[0], rows[2:] if len(rows) > 2 else []
            out.append("<table><thead><tr>" +
                       "".join(f"<th>{inline(c)}</th>" for c in head) +
                       "</tr></thead><tbody>")
            for r in body:
                out.append("<tr>" + "".join(f"<td>{inline(c)}</td>" for c in r) + "</tr>")
            out.append("</tbody></table>")

        elif line.startswith(("- ", "* ")):
            items = []
            while i < len(lines) and lines[i].startswith(("- ", "* ")):
                items.append(lines[i][2:])
                i += 1
            i -= 1
            out.append("<ul>" + "".join(f"<li>{inline(x)}</li>" for x in items) + "</ul>")

        elif re.match(r"^\d+\. ", line):
            items = []
            while i < len(lines) and re.match(r"^\d+\. ", lines[i]):
                items.append(re.sub(r"^\d+\. ", "", lines[i]))
                i += 1
            i -= 1
            out.append("<ol>" + "".join(f"<li>{inline(x)}</li>" for x in items) + "</ol>")

        elif line.strip():
            para = []
            while i < len(lines) and lines[i].strip() and \
                    not lines[i].startswith(("#", "```", "|", "- ", "* ")):
                para.append(lines[i])
                i += 1
            i -= 1
            out.append("<p>" + inline(" ".join(para)) + "</p>")

        i += 1

    return "\n".join(out)


# --- pages -------------------------------------------------------------------

def parse(path):
    raw = path.read_text()
    if not raw.startswith("---\n"):
        raise SystemExit(f"{path}: no header block")

    _, header, body = raw.split("---\n", 2)
    meta, key = {}, None
    for line in header.split("\n"):
        if re.match(r"^\s", line) and key:          # a wrapped value
            meta[key] += " " + line.strip()
        elif ":" in line:
            key, value = line.split(":", 1)
            key = key.strip()
            meta[key] = value.strip()

    for required in ("title", "description"):
        if not meta.get(required):
            raise SystemExit(f"{path}: no {required}. Search results need one.")
    if len(meta["description"]) > 200:
        raise SystemExit(f"{path}: description over 200 characters; it will be cut off.")

    return meta, body


def url_for(rel):
    """index.md at the root is /; everything else is a directory with a
    trailing slash, so the URL has no .html in it."""
    if rel.name == "index.md":
        return "/" + str(rel.parent).replace(".", "").lstrip("/") + \
               ("/" if str(rel.parent) != "." else "")
    return "/" + str(rel.with_suffix("")).lstrip("/") + "/"


def render(meta, body_html, url):
    tmpl = (ROOT / "templates" / "base.html").read_text()
    canonical = CFG["domain"].rstrip("/") + url

    schema = []
    if meta.get("kind") == "article":
        schema.append(json.dumps({
            "@context": "https://schema.org",
            "@type": "TechArticle",
            "headline": meta["title"],
            "description": meta["description"],
            "datePublished": meta.get("date", str(date.today())),
            "author": {"@type": "Person", "name": CFG["author"]},
            "mainEntityOfPage": canonical,
        }, indent=2))
    if meta.get("schema") == "product":
        schema.append(json.dumps({
            "@context": "https://schema.org",
            "@type": "Product",
            "name": CFG["product"],
            "description": CFG["tagline"],
            "brand": {"@type": "Brand", "name": "silt"},
            "offers": {
                "@type": "Offer",
                "price": CFG["price"],
                "priceCurrency": CFG["currency"],
                "url": CFG["buy_url"],
                "availability": "https://schema.org/InStock",
            },
        }, indent=2))

    blocks = "\n".join(
        f'<script type="application/ld+json">{s}</script>' for s in schema)

    date_line = ""
    if meta.get("kind") == "article" and meta.get("date"):
        date_line = f'<p class="meta"><time datetime="{meta["date"]}">{meta["date"]}</time></p>'

    return (tmpl
            .replace("{{title}}", html.escape(meta["title"]))
            .replace("{{description}}", html.escape(meta["description"]))
            .replace("{{canonical}}", canonical)
            .replace("{{product}}", html.escape(CFG["product"]))
            .replace("{{price}}", CFG["price"])
            .replace("{{buy_url}}", CFG["buy_url"])
            .replace("{{email}}", CFG["contact_email"])
            .replace("{{year}}", str(date.today().year))
            .replace("{{schema}}", blocks)
            .replace("{{date}}", date_line)
            .replace("{{body}}", body_html))


def build():
    if OUT.exists():
        shutil.rmtree(OUT)
    OUT.mkdir()

    pages = []
    for path in sorted((ROOT / "content").rglob("*.md")):
        rel = path.relative_to(ROOT / "content")
        meta, body = parse(path)
        url = url_for(rel)

        body_html = markdown(body)
        body_html = (body_html
                     .replace("{{price}}", CFG["price"])
                     .replace("{{buy_url}}", CFG["buy_url"])
                     .replace("{{product}}", html.escape(CFG["product"]))
                     .replace("{{tagline}}", html.escape(CFG["tagline"])))

        target = OUT / url.strip("/") / "index.html" if url != "/" else OUT / "index.html"
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_text(render(meta, body_html, url))
        pages.append((url, meta))
        print(f"  {url:40s} {meta['title'][:44]}")

    for item in (ROOT / "static").iterdir():
        shutil.copy(item, OUT / item.name)

    urls = "\n".join(
        f"  <url><loc>{CFG['domain'].rstrip('/')}{u}</loc>"
        f"<lastmod>{m.get('date', date.today())}</lastmod></url>"
        for u, m in pages)
    (OUT / "sitemap.xml").write_text(
        '<?xml version="1.0" encoding="UTF-8"?>\n'
        '<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">\n'
        f"{urls}\n</urlset>\n")

    (OUT / "robots.txt").write_text(
        f"User-agent: *\nAllow: /\n\n"
        f"Sitemap: {CFG['domain'].rstrip('/')}/sitemap.xml\n")

    print(f"\n{len(pages)} pages into {OUT}")
    if "example.com" in CFG["domain"] or "CHANGE-ME" in CFG["buy_url"]:
        print("note: config.json still has placeholders for the domain or the "
              "buy link. They go into canonical URLs, the sitemap and the "
              "schema markup, so fix them before deploying.")


if __name__ == "__main__":
    build()
    if "--serve" in sys.argv:
        import http.server
        import os
        os.chdir(OUT)
        print("http://localhost:8000")
        http.server.test(HandlerClass=http.server.SimpleHTTPRequestHandler,
                         port=8000, bind="127.0.0.1")
