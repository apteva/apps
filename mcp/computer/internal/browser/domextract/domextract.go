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
  const clean = (s) => String(s || '').replace(/\s+/g, ' ').trim();
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
      .map((el) => ({ el, len: clean(el.innerText || el.textContent || '').length }))
      .sort((a, b) => b.len - a.len)[0].el;
  }
  for (const el of Array.from(root.querySelectorAll('nav,header,footer,aside,script,style,noscript,svg,template'))) {
    el.remove();
  }

  const text = clean(root.innerText || root.textContent || '');
  const markdown = text.split(/\n{2,}/).map(clean).filter(Boolean).join('\n\n') || text;
  const description = meta.description || meta['og:description'] || meta['twitter:description'] || '';
  const links = Array.from(document.querySelectorAll('a[href]')).map((a) => ({
    url: abs(a.getAttribute('href')),
    text: clean(a.innerText || a.textContent || a.getAttribute('aria-label') || a.getAttribute('title') || '')
  })).filter((x) => /^https?:\/\//i.test(x.url));
  const images = Array.from(document.querySelectorAll('img[src],source[srcset]')).map((img) => {
    const raw = img.getAttribute('src') || String(img.getAttribute('srcset') || '').split(/\s+/)[0];
    return abs(raw);
  }).filter(Boolean);
  return {
    url: location.href,
    title: clean(document.title || meta['og:title'] || meta['twitter:title'] || ''),
    description,
    text,
    markdown,
    html: root.innerHTML || '',
    links,
    images,
    metadata: meta,
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
