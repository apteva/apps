package main

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

type tradingContract struct {
	AppOnlyTools     []string `json:"app_only_tools"`
	Tools            []string `json:"tools"`
	OrderPlaceFields []string `json:"order_place_fields"`
}

func loadTradingContract(t *testing.T) tradingContract {
	t.Helper()
	raw, err := os.ReadFile("trading_contract.json")
	if err != nil {
		t.Fatal(err)
	}
	var c tradingContract
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatal(err)
	}
	return c
}

// tradingCallsInSource returns every Trading tool name Strategies' non-test
// source passes as a string literal to callTrading or to a platform call
// addressed to the "trading" app, plus named constants used for the same.
func tradingCallsInSource(t *testing.T) map[string]string {
	t.Helper()
	files, _ := filepath.Glob("*.go")
	consts := map[string]string{}
	var parsed []*ast.File
	fset := token.NewFileSet()
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		parsed = append(parsed, f)
		ast.Inspect(f, func(n ast.Node) bool {
			if vs, ok := n.(*ast.ValueSpec); ok {
				for i, name := range vs.Names {
					if i < len(vs.Values) {
						if lit, ok := vs.Values[i].(*ast.BasicLit); ok && lit.Kind == token.STRING {
							consts[name.Name], _ = strconv.Unquote(lit.Value)
						}
					}
				}
			}
			return true
		})
	}
	str := func(e ast.Expr) (string, bool) {
		switch e := e.(type) {
		case *ast.BasicLit:
			if e.Kind == token.STRING {
				s, err := strconv.Unquote(e.Value)
				return s, err == nil
			}
		case *ast.Ident:
			s, ok := consts[e.Name]
			return s, ok
		}
		return "", false
	}
	out := map[string]string{}
	for _, f := range parsed {
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			var fn string
			switch c := call.Fun.(type) {
			case *ast.Ident:
				fn = c.Name
			case *ast.SelectorExpr:
				fn = c.Sel.Name
			}
			pos := fset.Position(call.Pos()).String()
			switch fn {
			case "callTrading":
				if len(call.Args) >= 2 {
					if name, ok := str(call.Args[1]); ok {
						out[name] = pos
					}
				}
			case "CallAppResult", "CallApp":
				if len(call.Args) >= 2 {
					if app, ok := str(call.Args[0]); ok && app == "trading" {
						if name, ok := str(call.Args[1]); ok {
							out[name] = pos
						}
					}
				}
			case "CallEnvironmentAppResult", "CallEnvironmentApp":
				if len(call.Args) >= 3 {
					if app, ok := str(call.Args[1]); ok && app == "trading" {
						if name, ok := str(call.Args[2]); ok {
							out[name] = pos
						}
					}
				}
			}
			return true
		})
	}
	return out
}

func TestTradingCallsAreInContract(t *testing.T) {
	c := loadTradingContract(t)
	allowed := map[string]bool{}
	for _, name := range append(append([]string{}, c.Tools...), c.AppOnlyTools...) {
		allowed[name] = true
	}
	calls := tradingCallsInSource(t)
	if len(calls) == 0 {
		t.Fatal("found no Trading calls; the source scan is broken")
	}
	for name, pos := range calls {
		if !allowed[name] {
			t.Errorf("%s calls Trading tool %q, which trading_contract.json does not list", pos, name)
		}
	}
	// Tools proxied by name through callTrading(ctx, name, ...) in MCPTools.
	for _, name := range []string{"portfolio_list", "portfolio_get", "account_summary", "positions_list", "orders_list", "market_quote", "market_history", "market_source", "journal_read"} {
		if !isTradingRead(name) {
			t.Fatalf("isTradingRead no longer proxies %s; update this test", name)
		}
		if !allowed[name] {
			t.Errorf("proxied Trading read %q is missing from trading_contract.json", name)
		}
	}
}

func TestOrderPlaceFieldsMatchContract(t *testing.T) {
	c := loadTradingContract(t)
	sent := append(append([]string{}, orderPlaceForwardedFields...), "external_strategy_id", "external_strategy_version", "decision_ref", "if_revision")
	sort.Strings(sent)
	want := append([]string{}, c.OrderPlaceFields...)
	sort.Strings(want)
	if strings.Join(sent, ",") != strings.Join(want, ",") {
		t.Fatalf("Strategies forwards %v to order_place; trading_contract.json lists %v", sent, want)
	}
}
