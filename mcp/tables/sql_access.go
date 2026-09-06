package main

import (
	"context"
	"database/sql"
	"sort"
	"strings"
	"unicode"

	sdk "github.com/apteva/app-sdk"
)

type sqlToken struct {
	start, end  int
	kind, value string
}

// SQLite strings, quoted identifiers and comments must not be interpreted as
// statement separators or placeholders. Token offsets retain the original SQL.
func sqlTokens(s string) ([]sqlToken, error) {
	tokens := []sqlToken{}
	for i := 0; i < len(s); {
		start := i
		c := s[i]
		if unicode.IsSpace(rune(c)) {
			i++
			continue
		}
		if i+1 < len(s) && s[i:i+2] == "--" {
			for i < len(s) && s[i] != '\n' {
				i++
			}
			continue
		}
		if i+1 < len(s) && s[i:i+2] == "/*" {
			end := strings.Index(s[i+2:], "*/")
			if end < 0 {
				return nil, errf("unterminated SQL comment")
			}
			i += end + 4
			continue
		}
		if c == '\'' || c == '"' || c == '`' || c == '[' {
			close := c
			kind := "identifier"
			if c == '\'' {
				kind = "string"
			}
			if c == '[' {
				close = ']'
			}
			i++
			var value strings.Builder
			closed := false
			for i < len(s) {
				if s[i] == close {
					if c != '[' && i+1 < len(s) && s[i+1] == close {
						value.WriteByte(close)
						i += 2
						continue
					}
					i++
					closed = true
					break
				}
				value.WriteByte(s[i])
				i++
			}
			if !closed {
				return nil, errf("unterminated SQL quote")
			}
			tokens = append(tokens, sqlToken{start, i, kind, value.String()})
			continue
		}
		if c == '{' {
			end := strings.IndexByte(s[i+1:], '}')
			if end < 0 {
				return nil, errf("unresolved {placeholder}")
			}
			i += end + 2
			name := s[start+1 : i-1]
			if err := validateIdentifier("placeholder", name); err != nil {
				return nil, err
			}
			tokens = append(tokens, sqlToken{start, i, "placeholder", name})
			continue
		}
		if c == '}' {
			return nil, errf("unresolved {placeholder}")
		}
		if c == '_' || c > 127 || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') {
			i++
			for i < len(s) {
				d := s[i]
				if !(d == '_' || d > 127 || (d >= 'a' && d <= 'z') || (d >= 'A' && d <= 'Z') || (d >= '0' && d <= '9')) {
					break
				}
				i++
			}
			tokens = append(tokens, sqlToken{start, i, "identifier", s[start:i]})
			continue
		}
		i++
		tokens = append(tokens, sqlToken{start, i, "symbol", s[start:i]})
	}
	return tokens, nil
}
func placeholderNames(s string) ([]string, error) {
	tokens, err := sqlTokens(s)
	if err != nil {
		return nil, err
	}
	set := map[string]bool{}
	for _, t := range tokens {
		if t.kind == "placeholder" {
			set[t.value] = true
		}
	}
	names := []string{}
	for n := range set {
		names = append(names, n)
	}
	sort.Strings(names)
	return names, nil
}

// The driver has no public authorizer API. Inspect SQLite's compiled program
// before executing it, allowing only B-tree roots of explicitly resolved tables
// and their indexes, plus the built-in JSON readers identified on this connection.
// Other virtual tables and dynamic roots fail closed.
// query_only remains enabled as an independent write barrier.
func authorizeQuery(qctx context.Context, conn *sql.Conn, ctx *sdk.AppCtx, a *App, pid, raw, resolved string, args []any) error {
	names, err := placeholderNames(raw)
	if err != nil {
		return err
	}
	allowed := map[int64]bool{}
	for _, name := range names {
		table, err := a.loadTableSchema(ctx, pid, name)
		if err != nil {
			return err
		}
		roots, err := conn.QueryContext(qctx, "SELECT rootpage FROM sqlite_master WHERE (type='table' AND name=?) OR (type='index' AND tbl_name=?)", table.PhysicalName, table.PhysicalName)
		if err != nil {
			return err
		}
		for roots.Next() {
			var root int64
			if err = roots.Scan(&root); err != nil {
				roots.Close()
				return err
			}
			allowed[root] = true
		}
		err = roots.Err()
		roots.Close()
		if err != nil {
			return err
		}
	}
	plan, err := conn.QueryContext(qctx, "EXPLAIN "+resolved, args...)
	if err != nil {
		return err
	}
	defer plan.Close()
	virtualReaders := map[string]bool{}
	for plan.Next() {
		var addr, p1, p2, p3, p5 int64
		var opcode string
		var p4, comment any
		if err := plan.Scan(&addr, &opcode, &p1, &p2, &p3, &p4, &p5, &comment); err != nil {
			return err
		}
		switch opcode {
		case "OpenRead", "ReopenIdx":
			if p3 != 0 || p5&16 != 0 || !allowed[p2] {
				return errf("query accesses a table outside the requested project placeholders")
			}
		case "VOpen":
			identity := stringValue(p4)
			if !strings.HasPrefix(identity, "vtab:") || len(identity) == len("vtab:") {
				return errf("unrecognized virtual table in tables_query")
			}
			virtualReaders[identity] = true
		case "VUpdate", "VCreate", "VDestroy":
			return errf("virtual tables are not available in tables_query")
		case "OpenWrite", "CreateBtree", "Destroy", "Clear", "SetCookie", "Vacuum", "JournalMode", "Checkpoint", "ParseSchema", "LoadAnalysis", "SqlExec":
			return errf("only read-only SELECT statements are allowed")
		case "Function", "PureFunc":
			f := strings.ToLower(strings.TrimSpace(stringValue(p4)))
			if strings.HasPrefix(f, "load_extension(") || strings.HasPrefix(f, "sqlite_attach(") || strings.HasPrefix(f, "sqlite_detach(") || strings.HasPrefix(f, "readfile(") || strings.HasPrefix(f, "writefile(") {
				return errf("function not permitted in tables_query")
			}
		}
	}
	if err := plan.Err(); err != nil {
		return err
	}
	if err := plan.Close(); err != nil {
		return err
	}
	if len(virtualReaders) == 0 {
		return nil
	}
	allowedReaders, err := jsonReaderIdentities(qctx, conn)
	if err != nil {
		return err
	}
	for identity := range virtualReaders {
		if !allowedReaders[identity] {
			return errf("only built-in json_each and json_tree virtual tables are available in tables_query")
		}
	}
	return nil
}
func stringValue(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case []byte:
		return string(x)
	}
	return ""
}

// VOpen.P4 is SQLite's virtual-table identity, not the SQL alias or function
// spelling (https://sqlite.org/opcode.html#VOpen). Derive the two permitted
// identities from trusted, schema-qualified EXPLAIN statements on the SAME
// connection. Never cache these addresses across connections or dereference them.
// Probes only compile constant JSON; they do not execute caller data or modules.
func jsonReaderIdentities(ctx context.Context, conn *sql.Conn) (map[string]bool, error) {
	allowed := map[string]bool{}
	for _, name := range []string{"json_each", "json_tree"} {
		// A persisted table/view with this name could shadow an eponymous built-in.
		// Do not trust its VOpen identity, even if it is another virtual table.
		var shadowed int
		if err := conn.QueryRowContext(ctx, "SELECT count(*) FROM main.sqlite_schema WHERE type IN ('table','view') AND name=? COLLATE NOCASE", name).Scan(&shadowed); err != nil {
			return nil, err
		}
		if shadowed != 0 {
			continue
		}
		plan, err := conn.QueryContext(ctx, "EXPLAIN SELECT value FROM main."+name+"('[]')")
		if err != nil {
			return nil, err
		}
		found := false
		for plan.Next() {
			var addr, p1, p2, p3, p5 int64
			var opcode string
			var p4, comment any
			if err := plan.Scan(&addr, &opcode, &p1, &p2, &p3, &p4, &p5, &comment); err != nil {
				plan.Close()
				return nil, err
			}
			if opcode == "VOpen" {
				identity := stringValue(p4)
				if !strings.HasPrefix(identity, "vtab:") || len(identity) == len("vtab:") {
					plan.Close()
					return nil, errf("cannot identify built-in JSON reader")
				}
				allowed[identity] = true
				found = true
			}
		}
		err = plan.Err()
		plan.Close()
		if err != nil {
			return nil, err
		}
		if !found {
			return nil, errf("cannot identify built-in JSON reader")
		}
	}
	return allowed, nil
}
