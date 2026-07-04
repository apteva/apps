package domextract

import (
	"context"
	"strings"
	"time"
	"unicode/utf8"

	computer "github.com/apteva/apps/mcp/computer/internal/browser/api"
	"github.com/chromedp/chromedp"
)

const defaultMaxChars = 50000

const extractScript = `(() => {
  const cleanInline = (s) => String(s || '').replace(/\s+/g, ' ').trim();
  const normalizeBlocks = (s) => String(s || '')
    .replace(/\r/g, '\n')
    .split(/\n+/)
    .map(cleanInline)
    .filter(Boolean)
    .join('\n\n');
  const abs = (raw) => {
    try {
      return new URL(raw, location.href).href;
    } catch (_) {
      return '';
    }
  };
  const meta = {};
  for (const m of Array.from(document.querySelectorAll('meta'))) {
    const key = (m.getAttribute('name') || m.getAttribute('property') || '').trim().toLowerCase();
    const value = (m.getAttribute('content') || '').trim();
    if (key && value && !meta[key]) meta[key] = value;
  }
  const canonical = document.querySelector('link[rel~="canonical"]');
  if (canonical && canonical.href) meta.canonical = canonical.href;
  const metaBucket = (prefix) => Object.fromEntries(Object.entries(meta)
    .filter(([k]) => k.startsWith(prefix))
    .map(([k, v]) => [k.slice(prefix.length), v]));
  const jsonLd = [];
  for (const script of Array.from(document.querySelectorAll('script[type="application/ld+json"]'))) {
    const raw = script.textContent || '';
    if (!raw.trim()) continue;
    try {
      const parsed = JSON.parse(raw);
      if (Array.isArray(parsed)) jsonLd.push(...parsed);
      else jsonLd.push(parsed);
    } catch (err) {
      jsonLd.push({ parse_error: String(err && err.message || err), raw: raw.slice(0, 1000) });
    }
  }

  const clone = document.body ? document.body.cloneNode(true) : document.createElement('body');
  for (const el of Array.from(clone.querySelectorAll('script,style,noscript,svg,template,iframe,canvas'))) {
    el.remove();
  }
  for (const el of Array.from(clone.querySelectorAll('[hidden],[aria-hidden="true"]'))) {
    el.remove();
  }

  const candidates = Array.from(clone.querySelectorAll('article,main,[role="main"],#content,.content,.article,.post'));
  let root = clone;
  if (__READABILITY__ && candidates.length) {
    root = candidates
      .map((el) => ({ el, len: cleanInline(el.innerText || el.textContent || '').length }))
      .sort((a, b) => b.len - a.len)[0].el;
  }
  for (const el of Array.from(root.querySelectorAll('nav,header,footer,aside,script,style,noscript,svg,template'))) {
    el.remove();
  }

  const markdownBlocks = [];
  const pushBlock = (s) => {
    const t = cleanInline(s);
    if (t) markdownBlocks.push(t);
  };
  const walkMarkdown = (node) => {
    if (!node || node.nodeType !== Node.ELEMENT_NODE) return;
    const tag = node.tagName.toLowerCase();
    if (['script','style','noscript','svg','template','iframe','canvas','nav','header','footer','aside'].includes(tag)) return;
    if (/^h[1-6]$/.test(tag)) {
      const level = Number(tag.slice(1));
      pushBlock('#'.repeat(level) + ' ' + (node.innerText || node.textContent || ''));
      return;
    }
    if (tag === 'p' || tag === 'blockquote') {
      pushBlock(node.innerText || node.textContent || '');
      return;
    }
    if (tag === 'li') {
      pushBlock('- ' + (node.innerText || node.textContent || ''));
      return;
    }
    if (tag === 'pre') {
      const code = String(node.innerText || node.textContent || '').trim();
      if (code) markdownBlocks.push('~~~\n' + code + '\n~~~');
      return;
    }
    if (tag === 'table') {
      const rows = Array.from(node.querySelectorAll('tr')).map((tr) =>
        Array.from(tr.children).map((cell) => cleanInline(cell.innerText || cell.textContent || '')).filter(Boolean).join(' | ')
      ).filter(Boolean);
      if (rows.length) markdownBlocks.push(rows.join('\n'));
      return;
    }
    for (const child of Array.from(node.children)) walkMarkdown(child);
  };
  walkMarkdown(root);
  const text = normalizeBlocks(root.innerText || root.textContent || '');
  const markdown = markdownBlocks.length ? markdownBlocks.join('\n\n') : text;
  const description = meta.description || meta['og:description'] || meta['twitter:description'] || '';
  const links = Array.from(document.querySelectorAll('a[href]')).map((a) => ({
    url: abs(a.getAttribute('href')),
    text: cleanInline(a.innerText || a.textContent || a.getAttribute('aria-label') || a.getAttribute('title') || '')
  })).filter((x) => /^https?:\/\//i.test(x.url));
  const images = Array.from(document.querySelectorAll('img[src],source[srcset]')).map((img) => {
    const raw = img.getAttribute('src') || String(img.getAttribute('srcset') || '').split(/\s+/)[0];
    return abs(raw);
  }).filter(Boolean);
  return {
    url: location.href,
    title: cleanInline(document.title || meta['og:title'] || meta['twitter:title'] || ''),
    description,
    text,
    markdown,
    html: root.innerHTML || '',
    links,
    images,
    metadata: meta,
    structured_data: {
      json_ld: jsonLd,
      open_graph: metaBucket('og:'),
      twitter: metaBucket('twitter:'),
      canonical: meta.canonical || '',
      title: cleanInline(document.title || meta['og:title'] || meta['twitter:title'] || ''),
      description
    },
    rendered: true,
    extraction_backend: 'browser_dom'
  };
})()`

// Run extracts structured content from the active CDP target.
func Run(ctx context.Context, opts computer.ExtractOptions) (computer.ExtractResult, error) {
	if opts.WaitMS > 0 {
		time.Sleep(time.Duration(opts.WaitMS) * time.Millisecond)
	}
	var out computer.ExtractResult
	readability := "false"
	if opts.Readability {
		readability = "true"
	}
	script := strings.ReplaceAll(extractScript, "__READABILITY__", readability)
	if err := chromedp.Run(ctx, chromedp.Evaluate(script, &out)); err != nil {
		return computer.ExtractResult{}, err
	}
	out.ExtractionBackend = "browser_dom"
	out.Rendered = true
	max := opts.MaxChars
	if max <= 0 {
		max = defaultMaxChars
	}
	out.Text = truncate(out.Text, max)
	out.Markdown = truncate(out.Markdown, max)
	out.HTML = truncate(out.HTML, max)
	out.Links = dedupeLinks(out.Links, 1000)
	out.Images = dedupeStrings(out.Images, 500)
	filterFormats(&out, opts.Formats)
	return out, nil
}

func filterFormats(out *computer.ExtractResult, formats []string) {
	if len(formats) == 0 {
		return
	}
	want := map[string]bool{}
	for _, f := range formats {
		want[strings.ToLower(strings.TrimSpace(f))] = true
	}
	if !want["text"] {
		out.Text = ""
	}
	if !want["markdown"] {
		out.Markdown = ""
	}
	if !want["html"] {
		out.HTML = ""
	}
	if !want["links"] {
		out.Links = nil
	}
	if !want["images"] {
		out.Images = nil
	}
	if !want["metadata"] {
		out.Metadata = nil
	}
	if !want["structured_data"] && !want["json"] {
		out.StructuredData = nil
	}
}

func truncate(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	for max > 0 && !utf8.RuneStart(s[max]) {
		max--
	}
	if max <= 0 {
		return ""
	}
	return s[:max]
}

func dedupeLinks(in []computer.ExtractLink, limit int) []computer.ExtractLink {
	seen := map[string]bool{}
	out := make([]computer.ExtractLink, 0, len(in))
	for _, link := range in {
		link.URL = strings.TrimSpace(link.URL)
		link.Text = truncate(strings.TrimSpace(link.Text), 200)
		if link.URL == "" || seen[link.URL] {
			continue
		}
		seen[link.URL] = true
		out = append(out, link)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out
}

func dedupeStrings(in []string, limit int) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out
}
