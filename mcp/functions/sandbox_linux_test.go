//go:build linux

package main

import (
	"context"
	"strings"
	"testing"

	tk "github.com/apteva/app-sdk/testkit"
)

func TestLinuxWorkerCannotTamperArtifactOrReadProc(t *testing.T) {
	requireBin(t, "node")
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj))
	app := mountApp(t, ctx)
	fn := createFn(t, app, ctx, map[string]any{
		"name": "sandbox-fs",
		"source": `import fs from "node:fs";
			export default async (_event, context) => {
				let writeDenied = false, procDenied = false;
				try { fs.writeFileSync(context.env.APTEVA_FN_ENTRY, "tampered"); } catch { writeDenied = true; }
				try { fs.readFileSync("/proc/1/environ"); } catch { procDenied = true; }
				return { writeDenied, procDenied };
			};`,
	})
	res, err := invokeFunction(ctx, context.Background(), fn, nil, "manual")
	if err != nil || res.Status != "ok" {
		t.Fatalf("invoke: res=%+v err=%v", res, err)
	}
	if !strings.Contains(res.Response, `"writeDenied":true`) || !strings.Contains(res.Response, `"procDenied":true`) {
		t.Fatalf("worker escaped filesystem policy: %s", res.Response)
	}
}
