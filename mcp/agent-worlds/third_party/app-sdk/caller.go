package sdk

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// Caller is the per-MCP-call authorization context. It carries the
// calling agent's id and the grants the platform issued for this
// (install, agent) pair. Tool handlers retrieve it via CallerFrom(ctx)
// inside a HandlerCtx.
//
// A nil Caller means "no caller info was supplied" — the SDK treats
// this as full access (back-compat with platforms that don't yet
// forward the X-Apteva-Caller-Agent header). Apps that want to fail
// closed on missing caller info should check for nil explicitly.
type Caller struct {
	// AgentID is the calling agent's id. Zero when no caller info was
	// supplied.
	AgentID int64
	// ThreadID is the opaque id of the agent thread that made this MCP
	// call. The platform does not classify the thread (for example as a
	// conversation or worker); applications may attach their own meaning to
	// the id. Empty means the upstream did not provide thread context.
	ThreadID string
	// ThreadRole is a platform-owned classification of the caller's thread,
	// for example "main", "conversation", or "worker". Apps may use it to
	// enforce structural tool ownership. Empty means an older platform did not
	// provide the classification.
	ThreadRole string
	// ToolCallID is the stable identity of the MCP tool invocation. It lets an
	// app suppress duplicate visible effects when the platform retries a call.
	// It is supplied by the gateway, never read from model arguments.
	ToolCallID string
	// ProjectID is the authenticated project scope selected by the platform
	// gateway for this call. It is never read from model-supplied arguments.
	ProjectID string
	// AppInstallID and AppName identify an authenticated sibling app call.
	// They are populated only from server-owned X-Apteva-Bound-Caller-*
	// headers. Agent calls leave both fields empty/zero.
	AppInstallID int64
	AppName      string
	// SubjectType and SubjectID identify a delegated end user. They are
	// populated exclusively from server-owned X-Apteva-Subject-* headers.
	// Agent calls leave these fields empty.
	SubjectType      string
	SubjectID        string
	SubjectEmail     string
	OrganizationID   string
	OrganizationSlug string
	// Grants is the policy fetched for (this install, this agent).
	// Nil + non-nil with len 0 are the same — both mean "no rules";
	// the install's DefaultEffect determines whether that's allow or
	// deny.
	Grants []Grant
	// DefaultEffect is "allow" or "deny" when no rule matches. Set
	// by the platform; defaults to "allow" for back-compat.
	DefaultEffect string
	// Resources mirrors Provides.Resources from the manifest so the
	// Caller's matcher knows how to compare grant.resource to runtime
	// resource. The framework populates this from ctx.Manifest().
	Resources []ResourceDecl
}

// Grant is one rule in the caller's policy.
type Grant struct {
	Effect     string `json:"effect"`     // "allow" | "deny"
	Permission string `json:"permission"` // matches a ProvidedPermission.Name
	Resource   string `json:"resource"`   // matcher-specific pattern
}

type callerKey struct{}

// CallerFrom pulls the caller out of the context.Context. Returns nil
// when the context wasn't built by the framework (test code that
// didn't use testkit, or callers from outside MCP).
func CallerFrom(ctx context.Context) *Caller {
	if ctx == nil {
		return nil
	}
	c, _ := ctx.Value(callerKey{}).(*Caller)
	return c
}

// WithCaller attaches a Caller to a context.Context. Used by the
// framework's MCP handler and by testkit. Apps should not call this
// directly.
func WithCaller(ctx context.Context, c *Caller) context.Context {
	return context.WithValue(ctx, callerKey{}, c)
}

// Allows reports whether the caller is permitted to invoke `permission`
// against `resource`. A nil Caller (no info supplied) returns true —
// pre-permission-system back-compat.
//
// Evaluation: explicit deny > explicit allow > DefaultEffect. The
// resource string is matched per the resource type's Matcher.
func (c *Caller) Allows(permission, resource string) bool {
	if c == nil {
		return true
	}
	allowed := false
	for _, g := range c.Grants {
		if g.Permission != permission && g.Permission != "*" {
			continue
		}
		if !c.matchResource(permission, resource, g.Resource) {
			continue
		}
		if g.Effect == "deny" {
			return false
		}
		if g.Effect == "allow" {
			allowed = true
		}
	}
	if allowed {
		return true
	}
	switch c.DefaultEffect {
	case "deny":
		return false
	default:
		return true
	}
}

// resourceTypeFor returns the ResourceDecl that backs `permission`,
// or nil when the permission is unparameterized or the manifest is
// missing.
func (c *Caller) resourceTypeFor(permission string) *ResourceDecl {
	// We don't carry the permission→resource map here; do a linear
	// scan over Resources. There are usually under 10. Could cache
	// per-Caller if it ever shows up in profiles.
	//
	// In practice the manifest passes the resource type id encoded
	// in the grant.resource itself for typed pattern recognition;
	// we rely on Matcher set on the ResourceDecl.
	if len(c.Resources) == 0 {
		return nil
	}
	// Find the resource type whose name appears as a prefix of the
	// runtime resource. Convention: callers pass resources as
	// "<resource_type>/<value>" (e.g. "folder/invoices/q3") so that
	// the Matcher can be picked deterministically.
	return nil
}

// matchResource compares a runtime `resource` against `pattern` for
// the given permission. Picks the matcher from the declared resource
// type. Falls back to glob when no resource type can be resolved.
func (c *Caller) matchResource(permission, resource, pattern string) bool {
	// "*" pattern is universal regardless of matcher.
	if pattern == "*" {
		return true
	}
	matcher := "glob"
	for i := range c.Resources {
		// Convention: resources are namespaced by their type name —
		// "folder/foo/bar", "account/tw_123". The runtime resource
		// is matched against grants whose pattern uses the same
		// namespace.
		prefix := c.Resources[i].Name + "/"
		if strings.HasPrefix(resource, prefix) || strings.HasPrefix(pattern, prefix) {
			matcher = c.Resources[i].Matcher
			break
		}
	}
	switch matcher {
	case "glob":
		return globMatch(pattern, resource)
	case "prefix":
		return strings.HasPrefix(resource, pattern)
	case "exact":
		return resource == pattern
	case "id_set":
		// Pattern is a comma-separated list of ids: "tw_123,tw_456".
		// Resource is a single id (already namespaced as
		// "account/tw_123"); compare on the post-prefix part.
		_, rid, _ := splitNamespacedResource(resource)
		for _, want := range strings.Split(pattern, ",") {
			_, w, _ := splitNamespacedResource(strings.TrimSpace(want))
			if w == "" {
				w = strings.TrimSpace(want)
			}
			if w == rid {
				return true
			}
		}
		return false
	case "tag_set":
		// Pattern: "marketing,public". Resource: "tag/marketing".
		_, rid, _ := splitNamespacedResource(resource)
		for _, want := range strings.Split(pattern, ",") {
			if strings.TrimSpace(want) == rid {
				return true
			}
		}
		return false
	}
	return false
}

func splitNamespacedResource(s string) (ns, id string, ok bool) {
	i := strings.IndexByte(s, '/')
	if i < 0 {
		return "", s, false
	}
	return s[:i], s[i+1:], true
}

// globMatch implements a small subset of shell glob — enough for
// folder-style hierarchical resources.
//
//   - * matches any run of non-slash characters
//   - ** matches any run of characters including slashes
//   - ? matches one non-slash character
//
// Patterns are anchored at both ends. Empty pattern matches empty
// resource. This is hand-rolled to avoid a heavy dependency for what
// is, ultimately, an authz hot path.
func globMatch(pattern, s string) bool {
	return globMatchAt(pattern, s, 0, 0)
}

func globMatchAt(p, s string, pi, si int) bool {
	for pi < len(p) {
		c := p[pi]
		switch {
		case c == '*':
			// double-star ("**") swallows slashes; single eats only
			// non-slash runs.
			doublestar := pi+1 < len(p) && p[pi+1] == '*'
			rest := pi + 1
			if doublestar {
				rest = pi + 2
			}
			// Try every possible split.
			for split := si; split <= len(s); split++ {
				if globMatchAt(p, s, rest, split) {
					return true
				}
				if split < len(s) && !doublestar && s[split] == '/' {
					// Single * can't cross a slash.
					return false
				}
			}
			return false
		case c == '?':
			if si >= len(s) || s[si] == '/' {
				return false
			}
			pi++
			si++
		default:
			if si >= len(s) || s[si] != c {
				return false
			}
			pi++
			si++
		}
	}
	return si == len(s)
}

// Has is the unparameterized form — "does the caller hold this
// permission at all, on any resource?". Useful for pre-checks before
// expensive list builds.
func (c *Caller) Has(permission string) bool {
	if c == nil {
		return true
	}
	for _, g := range c.Grants {
		if (g.Permission == permission || g.Permission == "*") && g.Effect == "allow" {
			return true
		}
	}
	return c.DefaultEffect != "deny"
}

// Filter returns the items the caller is permitted to read under
// `permission`. resourceFn maps each item to its namespaced resource
// string ("folder/<path>", "account/<id>"). Items the caller doesn't
// own are silently dropped — it's a list filter, not an enforcement
// gate; explicit-deny items return false from Allows but Filter
// just omits them.
func Filter[T any](c *Caller, permission string, items []T, resourceFn func(T) string) []T {
	if c == nil {
		return items
	}
	out := make([]T, 0, len(items))
	for _, it := range items {
		if c.Allows(permission, resourceFn(it)) {
			out = append(out, it)
		}
	}
	return out
}

// FilterTree is the tree-aware variant for hierarchical resources
// (folders, code paths). It returns:
//
//   - items entirely inside the caller's scope
//   - ancestor stubs needed to reach those items (so the caller can
//     navigate from root to their allowed subtree)
//
// resourceFn returns the namespaced resource for each item; itemPath
// returns the path-without-namespace ("invoices/q3" for an item whose
// resource is "folder/invoices/q3"). Both are needed because the
// ancestor check works in path space, not resource space.
//
// Behavior depends on the resource's listing_visibility:
//
//	navigable (default for tree) — ancestor stubs visible
//	scoped_only                  — only items strictly inside scope
//	none                         — empty list
func FilterTree[T any](c *Caller, permission string, items []T, resourceFn func(T) string, itemPath func(T) string) []T {
	if c == nil {
		return items
	}
	visibility := "navigable"
	for _, r := range c.Resources {
		if r.Matcher == "glob" {
			if r.ListingVisibility != "" {
				visibility = r.ListingVisibility
			}
			break
		}
	}
	switch visibility {
	case "none":
		return nil
	case "scoped_only":
		return Filter(c, permission, items, resourceFn)
	}
	// navigable: collect both fully-allowed items + ancestors that
	// have any allowed descendant.
	allowedPrefixes := c.allowedPrefixes(permission)
	out := make([]T, 0, len(items))
	for _, it := range items {
		path := itemPath(it)
		if c.Allows(permission, resourceFn(it)) {
			out = append(out, it)
			continue
		}
		// Ancestor-stub check: the item is itself a prefix of (or
		// equal to) some allowed pattern's literal-prefix portion.
		// e.g. allowed "folder/invoices/**" → item "invoices/" is
		// an ancestor stub.
		for _, ap := range allowedPrefixes {
			if ap == "" {
				continue
			}
			if strings.HasPrefix(ap, path+"/") || ap == path {
				out = append(out, it)
				break
			}
		}
	}
	return out
}

// allowedPrefixes returns the literal-prefix portion of every allowed
// glob for `permission`. e.g. "folder/invoices/**" → "invoices",
// "folder/q3-*" → "q3-". Resource namespace is stripped.
func (c *Caller) allowedPrefixes(permission string) []string {
	var out []string
	for _, g := range c.Grants {
		if g.Effect != "allow" {
			continue
		}
		if g.Permission != permission && g.Permission != "*" {
			continue
		}
		// Strip the resource-type namespace ("folder/").
		_, rest, ok := splitNamespacedResource(g.Resource)
		if !ok {
			continue
		}
		// Cut at the first wildcard.
		idx := indexOfFirstWildcard(rest)
		if idx < 0 {
			out = append(out, rest)
		} else {
			out = append(out, rest[:idx])
		}
	}
	return out
}

func indexOfFirstWildcard(s string) int {
	for i := 0; i < len(s); i++ {
		if s[i] == '*' || s[i] == '?' {
			return i
		}
	}
	return -1
}

// ErrForbidden is the structured error apps return from a HandlerCtx
// when the caller fails an explicit Allows check. The framework
// surfaces it as an MCP -32000 error with a stable code prefix so
// clients can distinguish authz failures from genuine tool errors.
type ErrForbidden struct {
	Permission string
	Resource   string
}

func (e *ErrForbidden) Error() string {
	if e.Resource == "" {
		return fmt.Sprintf("forbidden: %s", e.Permission)
	}
	return fmt.Sprintf("forbidden: %s on %s", e.Permission, e.Resource)
}

// Forbidden builds an *ErrForbidden for handler use:
//
//	if !sdk.CallerFrom(ctx).Allows("files.delete", res) {
//	    return nil, sdk.Forbidden("files.delete", res)
//	}
func Forbidden(permission, resource string) error {
	return &ErrForbidden{Permission: permission, Resource: resource}
}

// IsForbidden reports whether err is (or wraps) an *ErrForbidden.
func IsForbidden(err error) bool {
	var f *ErrForbidden
	return errors.As(err, &f)
}

// substituteResource expands a ResourceFrom template (e.g.
// "folder/{arg.folder}") against the call args. Substitutions
// recognized:
//
//	{arg.<name>}    — the named arg, stringified
//	{arg.<name>?}   — same, but missing keys substitute "" without
//	                  failing. (Use sparingly; usually you want missing
//	                  args to fail closed.)
//
// Returns the literal template when no substitutions appear so apps
// can declare unparameterized resources like "all" if they want.
func substituteResource(template string, args map[string]any) (string, error) {
	if template == "" {
		return "", nil
	}
	var b strings.Builder
	i := 0
	for i < len(template) {
		if template[i] == '{' {
			end := strings.IndexByte(template[i:], '}')
			if end < 0 {
				return "", fmt.Errorf("unterminated substitution in %q", template)
			}
			expr := template[i+1 : i+end]
			i += end + 1
			optional := strings.HasSuffix(expr, "?")
			if optional {
				expr = expr[:len(expr)-1]
			}
			if !strings.HasPrefix(expr, "arg.") {
				return "", fmt.Errorf("only {arg.X} substitutions supported, got {%s}", expr)
			}
			key := expr[len("arg."):]
			val, ok := args[key]
			if !ok {
				if optional {
					continue
				}
				return "", fmt.Errorf("arg %q required for resource_from %q", key, template)
			}
			b.WriteString(fmt.Sprint(val))
			continue
		}
		b.WriteByte(template[i])
		i++
	}
	return b.String(), nil
}
