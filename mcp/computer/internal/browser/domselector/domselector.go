package domselector

// UniqueCSSPathFunction is embedded in browser-side action helpers. The
// returned selector is intentionally structural: presentation cues use it
// immediately after an action, so uniqueness matters more than long-term DOM
// stability. In particular, a bare ancestor path such as "form > input" can
// silently resolve to the first of several id-less inputs.
const UniqueCSSPathFunction = `
  function cssPath(el) {
    if (!el || !el.tagName) return '';
    var parts = [];
    for (var cur = el; cur && cur.nodeType === 1; cur = cur.parentElement) {
      if (cur.id) {
        var idSelector = '#' + CSS.escape(cur.id);
        try {
          if (document.querySelectorAll(idSelector).length === 1) {
            parts.unshift(idSelector);
            return parts.join(' > ');
          }
        } catch (_) {}
      }
      var tag = cur.tagName.toLowerCase();
      var part = tag;
      var parent = cur.parentElement;
      if (parent) {
        var sameTagIndex = 0;
        var sameTagCount = 0;
        for (var i = 0; i < parent.children.length; i++) {
          var sibling = parent.children[i];
          if (sibling.tagName && sibling.tagName.toLowerCase() === tag) {
            sameTagCount++;
            if (sibling === cur) sameTagIndex = sameTagCount;
          }
        }
        if (sameTagCount > 1) part += ':nth-of-type(' + sameTagIndex + ')';
      }
      parts.unshift(part);
      var candidate = parts.join(' > ');
      try {
        if (document.querySelectorAll(candidate).length === 1) return candidate;
      } catch (_) {}
    }
    return parts.join(' > ');
  }
`
