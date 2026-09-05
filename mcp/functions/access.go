package main

import (
	"encoding/json"
	"errors"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"strings"
)

// A nil policy preserves existing installations. An explicit empty list denies
// every call of that kind; app.tool and app.* identify app/tool permissions.
type FunctionAccess struct {
	Apps         []string `json:"apps"`
	Integrations []string `json:"integrations"`
}

func checkFunctionAccess(ctx *sdk.AppCtx, id int64, msg wireResponse) error {
	return checkFunctionAccessInPool(currentPool(), ctx, id, msg)
}
func checkFunctionAccessInPool(p *pool, ctx *sdk.AppCtx, id int64, msg wireResponse) error {
	var fn *Function
	var err error
	if p != nil {
		fn = p.cachedFunction(ctx.CurrentProject(), id, "")
	}
	if fn == nil {
		fn, err = dbGetFunction(ctx.AppDB(), ctx.CurrentProject(), id, "")
	}
	if err != nil {
		return err
	}
	if fn == nil {
		return errors.New("function deleted")
	}
	if fn.Access == nil {
		return nil
	}
	allowed := fn.Access.Apps
	target := msg.App + "." + msg.Tool
	if msg.Type == "integration" {
		allowed = fn.Access.Integrations
		var ref any
		if err := json.Unmarshal(msg.Conn, &ref); err != nil {
			return err
		}
		target = fmt.Sprint(ref) + "." + msg.Tool
	}
	for _, rule := range allowed {
		if rule == target || rule == "*" || (strings.HasSuffix(rule, ".*") && strings.HasPrefix(target, strings.TrimSuffix(rule, "*"))) {
			return nil
		}
	}
	return fmt.Errorf("function access policy denies %s", target)
}

func validateFunctionArgs(args map[string]any, create bool) error {
	for _, key := range []string{"source", "package_json", "source_kind", "runtime", "repo_path", "status", "name"} {
		if value, ok := args[key]; ok {
			str, valid := value.(string)
			if !valid {
				return fmt.Errorf("%s must be a string", key)
			}
			limit := 1 << 20
			if key == "package_json" {
				limit = 256 << 10
			}
			if len(str) > limit || strings.ContainsRune(str, 0) {
				return fmt.Errorf("%s is too large or contains NUL", key)
			}
		}
	}
	for _, key := range []string{"timeout_ms", "max_memory_mb", "repo_id"} {
		if raw, ok := args[key]; ok {
			n := int64Arg(args, key)
			var num float64
			switch x := raw.(type) {
			case float64:
				num = x
			case int:
				num = float64(x)
			case int64:
				num = float64(x)
			default:
				return fmt.Errorf("%s must be an integer", key)
			}
			if num != float64(n) || n <= 0 {
				return fmt.Errorf("%s must be a positive integer", key)
			}
			if key == "timeout_ms" && n > maxTimeoutMS {
				return errors.New("timeout_ms exceeds 300000")
			}
			if key == "max_memory_mb" && (n < 16 || n > maxMemoryMB) {
				return errors.New("max_memory_mb must be between 16 and 1024")
			}
		}
	}
	if raw, ok := args["env"]; ok {
		env, valid := raw.(map[string]any)
		if !valid {
			return errors.New("env must be an object")
		}
		size := 0
		for key, value := range env {
			v, ok := value.(string)
			if !ok || !safeFunctionEnvKey(key) || strings.TrimSpace(key) != key || strings.ContainsRune(v, 0) {
				return fmt.Errorf("invalid or reserved environment entry %q", key)
			}
			size += len(key) + len(v)
		}
		if size > 64<<10 {
			return errors.New("environment exceeds 64 KiB")
		}
	}
	if pkg, ok := args["package_json"].(string); ok && strings.TrimSpace(pkg) != "" {
		var obj map[string]any
		if json.Unmarshal([]byte(pkg), &obj) != nil || obj == nil {
			return errors.New("package_json must encode a JSON object")
		}
	}
	if raw, ok := args["access"]; ok && raw != nil {
		b, _ := json.Marshal(raw)
		var access FunctionAccess
		if err := json.Unmarshal(b, &access); err != nil {
			return errors.New("access requires apps and integrations string arrays")
		}
		for _, rules := range [][]string{access.Apps, access.Integrations} {
			if len(rules) > 100 {
				return errors.New("too many access rules")
			}
			for _, rule := range rules {
				if rule != "*" && (!strings.Contains(rule, ".") || strings.ContainsAny(rule, "/\\\x00 \n")) {
					return fmt.Errorf("invalid access rule %q", rule)
				}
			}
		}
	}
	return nil
}
