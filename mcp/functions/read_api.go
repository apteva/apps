package main

import "fmt"

func maskFunction(fn *Function) *Function {
	c := *withReadiness(fn)
	c.Env = map[string]string{}
	for k := range fn.Env {
		c.Env[k] = "[redacted]"
	}
	if fn.FunctionURL != nil {
		u := *fn.FunctionURL
		u.Token = ""
		c.FunctionURL = &u
	}
	return &c
}
func functionPage(rows []*Function, limit int) map[string]any {
	cursor := ""
	if len(rows) == limit {
		cursor = rows[len(rows)-1].Name
	}
	out := make([]*Function, 0, len(rows))
	for _, f := range rows {
		c := maskFunction(f)
		c.Source = ""
		c.Env = nil
		out = append(out, c)
	}
	return map[string]any{"functions": out, "count": len(out), "next_cursor": cursor}
}
func versionPage(rows []*FunctionVersion, limit int) map[string]any {
	cursor := ""
	if len(rows) == limit {
		cursor = fmt.Sprint(rows[len(rows)-1].Version)
	}
	return map[string]any{"versions": rows, "next_cursor": cursor}
}
func invocationPage(rows []*Invocation, limit int) map[string]any {
	cursor := ""
	if len(rows) == limit {
		cursor = fmt.Sprint(rows[len(rows)-1].ID)
	}
	out := make([]*Invocation, 0, len(rows))
	for _, r := range rows {
		c := *r
		c.EventJSON = ""
		c.ResponseBody = ""
		c.Stderr = ""
		out = append(out, &c)
	}
	return map[string]any{"invocations": out, "next_cursor": cursor}
}
