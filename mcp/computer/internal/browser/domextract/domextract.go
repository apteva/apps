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

  const textBlocks = [];
  const markdownBlocks = [];
  const pushTextBlock = (s) => {
    const t = cleanInline(s);
    if (t) textBlocks.push(t);
    return t;
  };
  const pushBlock = (s) => {
    const t = pushTextBlock(s);
    if (t) markdownBlocks.push(t);
  };
  const walkMarkdown = (node) => {
    if (!node || node.nodeType !== Node.ELEMENT_NODE) return;
    const tag = node.tagName.toLowerCase();
    if (['script','style','noscript','svg','template','iframe','canvas','nav','header','footer','aside'].includes(tag)) return;
    if (/^h[1-6]$/.test(tag)) {
      const level = Number(tag.slice(1));
      const heading = node.innerText || node.textContent || '';
      pushTextBlock(heading);
      const markdownText = cleanInline(heading);
      if (markdownText) markdownBlocks.push('#'.repeat(level) + ' ' + markdownText);
      return;
    }
    if (tag === 'p' || tag === 'blockquote') {
      pushBlock(node.innerText || node.textContent || '');
      return;
    }
    if (tag === 'a') {
      pushBlock(node.innerText || node.textContent || node.getAttribute('aria-label') || node.getAttribute('title') || '');
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
  const text = textBlocks.length ? textBlocks.join('\n\n') : normalizeBlocks(root.innerText || root.textContent || '');
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
  const cssIdent = (s) => {
    if (window.CSS && typeof window.CSS.escape === 'function') return window.CSS.escape(s);
    return String(s || '').replace(/[^a-zA-Z0-9_-]/g, '\\$&');
  };
  const selectorFor = (el) => {
    if (!el || el.nodeType !== Node.ELEMENT_NODE) return '';
    if (el.id) return '#' + cssIdent(el.id);
    const parts = [];
    let cur = el;
    while (cur && cur.nodeType === Node.ELEMENT_NODE && cur !== document.body && parts.length < 5) {
      let part = cur.tagName.toLowerCase();
      const cls = Array.from(cur.classList || []).filter(Boolean).slice(0, 2);
      if (cls.length) part += '.' + cls.map(cssIdent).join('.');
      const parent = cur.parentElement;
      if (parent) {
        const same = Array.from(parent.children).filter((x) => x.tagName === cur.tagName);
        if (same.length > 1) part += ':nth-of-type(' + (same.indexOf(cur) + 1) + ')';
      }
      parts.unshift(part);
      cur = parent;
    }
    return parts.join(' > ');
  };
  const nearestHeading = (el) => {
    const own = el.matches && el.matches('h1,h2,h3,h4,h5,h6') ? cleanInline(el.innerText || el.textContent || '') : '';
    if (own) return own;
    const inner = el.querySelector && el.querySelector('h1,h2,h3,h4,h5,h6');
    if (inner) {
      const t = cleanInline(inner.innerText || inner.textContent || '');
      if (t) return t;
    }
    let prev = el.previousElementSibling;
    let steps = 0;
    while (prev && steps < 4) {
      if (prev.matches && prev.matches('h1,h2,h3,h4,h5,h6')) {
        const t = cleanInline(prev.innerText || prev.textContent || '');
        if (t) return t;
      }
      prev = prev.previousElementSibling;
      steps++;
    }
    const labelled = el.getAttribute && el.getAttribute('aria-labelledby');
    if (labelled) {
      const lab = document.getElementById(labelled);
      const t = lab ? cleanInline(lab.innerText || lab.textContent || '') : '';
      if (t) return t;
    }
    return '';
  };
  const regionSelector = [
    'main','article','section','aside','footer','header','nav','form','table',
    '[role="main"]','[role="region"]','[role="contentinfo"]','[role="form"]','[role="article"]',
    '[class*="contact" i]','[id*="contact" i]','[class*="affiliate" i]','[id*="affiliate" i]',
    'h1','h2','h3','h4','[data-testid]','.card','.panel','.pricing'
  ].join(',');
  const regions = [];
  const seenEls = new Set();
  for (const el of Array.from(document.querySelectorAll(regionSelector))) {
    if (!el || seenEls.has(el)) continue;
    seenEls.add(el);
    const tag = el.tagName.toLowerCase();
    if (['script','style','noscript','svg','template'].includes(tag)) continue;
    const style = window.getComputedStyle(el);
    if (style.display === 'none' || style.visibility === 'hidden' || Number(style.opacity || '1') === 0) continue;
    const rect = el.getBoundingClientRect();
    const width = Math.max(0, rect.width);
    const height = Math.max(0, rect.height);
    if (width < 24 || height < 16) continue;
    const text = cleanInline(el.innerText || el.textContent || el.getAttribute('aria-label') || el.getAttribute('title') || '');
    const linkCount = el.querySelectorAll ? el.querySelectorAll('a[href]').length : 0;
    const imageCount = el.querySelectorAll ? el.querySelectorAll('img,source').length : 0;
    const isStructural = ['main','article','section','aside','footer','header','nav','form','table'].includes(tag) || (el.getAttribute('role') || '');
    if (text.length < 20 && !isStructural && linkCount === 0 && imageCount === 0) continue;
    regions.push({
      id: 'r' + (regions.length + 1),
      tag,
      role: el.getAttribute('role') || '',
      selector: selectorFor(el),
      heading: nearestHeading(el),
      text: text.slice(0, 1200),
      rect: {
        x: rect.left + window.scrollX,
        y: rect.top + window.scrollY,
        width,
        height
      },
      viewport_rect: {
        x: rect.left,
        y: rect.top,
        width,
        height
      },
      coordinate_frame: 'document_css_px',
      visible: rect.bottom > 0 && rect.right > 0 && rect.top < window.innerHeight && rect.left < window.innerWidth,
      link_count: linkCount,
      image_count: imageCount
    });
    if (regions.length >= 200) break;
  }
  return {
    url: location.href,
    title: cleanInline(document.title || meta['og:title'] || meta['twitter:title'] || ''),
    description,
    text,
    markdown,
    html: root.innerHTML || '',
    links,
    images,
    regions,
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
	if !want["regions"] {
		out.Regions = nil
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
