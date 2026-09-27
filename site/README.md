# The site

Six pages, no framework, no dependencies beyond Python 3. `build.py` turns
`content/*.md` into `public/`, and writes `sitemap.xml` and `robots.txt`.

```
./build.py            build
./build.py --serve    build, then http://localhost:8000
```

## Before deploying

`config.json` has two placeholders that go into canonical URLs, the sitemap
and the schema markup, so search engines see them:

- `domain` — the real domain, with https and no trailing slash
- `buy_url` — the Gumroad link

The build prints a reminder while either is still unset.

## Deploying

Anything that serves static files. Cloudflare Pages and GitHub Pages are both
free for this.

Cloudflare Pages: connect the repo, set the build command to
`cd site && python3 build.py` and the output directory to `site/public`.

GitHub Pages: build locally and push `site/public` to a `gh-pages` branch, or
add a workflow that runs the build.

Either way, point the domain at it and add the site to Google Search Console
the same day. Submit `https://yourdomain/sitemap.xml` there; that is the one
step that gets pages crawled in days rather than weeks.

## Writing a page

Markdown with a header block:

```
---
title: The thing someone would search for
description: One sentence, under 200 characters. Google shows this under
  the link, so write it for a person.
kind: article
date: 2026-09-22
---
```

`kind: article` adds the date and TechArticle schema. `schema: product` adds
Product schema with the price, and belongs only on the landing page.

The build refuses to write a page with no title or description, and refuses a
description long enough to be truncated in results.

## Supported Markdown

Headings, paragraphs, lists, numbered lists, tables, fenced code, links, bold
and inline code. `[label](url){.buy}` makes a button. That is the whole
subset; it is about sixty lines of `build.py` and easy to extend if you need
more.

## What to write next

The posts that will bring people here are the ones about problems, not about
the product. Write down the next thing that costs you an evening: the symptom
in the title, the explanation, the fix. Those pages rank because almost nobody
else writes them, and they are what make a reader trust the product enough to
buy it.
