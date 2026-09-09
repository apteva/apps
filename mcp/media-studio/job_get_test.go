package main

import (
	tk "github.com/apteva/app-sdk/testkit"
	"testing"
)

func TestMediaJobGetIsReadOnlyAndProjectScoped(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("a")).WithProject("a")
	res, err := ctx.AppDB().Exec(`INSERT INTO video_jobs(project_id,queue_id,provider,model,prompt,status,result_storage_id,generation_id) VALUES('a','queue','test','test','test','complete',77,8)`)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	a := &App{}
	got, err := a.toolMediaJobGet(ctx, map[string]any{"job_id": id})
	if err != nil {
		t.Fatal(err)
	}
	r := got.(map[string]any)
	if r["result_storage_id"] != int64(77) || r["cost_usd"] != nil {
		t.Fatal(r)
	}
	if _, err = a.toolMediaJobGet(ctx.WithProject("b"), map[string]any{"job_id": id}); err == nil {
		t.Fatal("cross-project job exposed")
	}
	if _, err = a.toolMediaJobGet(ctx, map[string]any{"job_id": id, "project_id": "b"}); err == nil {
		t.Fatal("project override accepted")
	}
	var count int
	ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM video_jobs`).Scan(&count)
	if count != 1 {
		t.Fatal("read created job")
	}
}
