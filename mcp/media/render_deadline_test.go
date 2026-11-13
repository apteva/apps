package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	tk "github.com/apteva/app-sdk/testkit"
)

func TestRenderQueueSurvivesLegacyAdmissionLimit(t *testing.T) {
	recorder := tk.NewEmitRecorder()
	app := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj), tk.WithEmitter(recorder), tk.WithConfig(map[string]string{"execution_mode": "component", "media_work_capacity": "2", "render_admission_timeout_seconds": "1"}))
	_, release, err := acquireMediaWork(context.Background(), app, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	id, err := insertRender(app.AppDB(), testProj, "crop", []string{"999"}, map[string]any{}, "image.png", "/", "tester")
	if err != nil {
		t.Fatal(err)
	}
	row, err := claimNextPending(app.AppDB())
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { runOneRender(app, row, nil, nil, 30); close(done) }()
	// Stay saturated beyond the former per-admission cutoff.
	select {
	case <-done:
		t.Fatal("capacity wait terminated before the overall deadline")
	case <-time.After(1200 * time.Millisecond):
	}
	var metrics map[string]any
	_ = json.Unmarshal(renderMetrics(app.AppDB(), id), &metrics)
	if metrics["execution_started"] != false || metrics["admission_queue_position"] != float64(1) || metrics["admission_capacity_used"] != float64(2) {
		t.Fatalf("missing queue evidence: %v", metrics)
	}
	if len(recorder.EventsByTopic(topicRenderStarted)) != 0 {
		t.Fatal("reported execution before admission")
	}
	release()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("original render did not resume")
	}
	// This intentionally unavailable executor fails only after admission. It
	// proves the same row reaches Execute once, with no resubmission or timeout.
	got, _ := getRender(app.AppDB(), testProj, id)
	if got.Status != "failed" || !strings.Contains(got.Error, "configured remote renderer is unavailable") {
		t.Fatalf("unexpected result: %+v", got)
	}
	if len(recorder.EventsByTopic(topicRenderStarted)) != 1 {
		t.Fatal("execution not started exactly once")
	}
	var n int
	_ = app.AppDB().QueryRow(`SELECT count(*) FROM renders`).Scan(&n)
	if n != 1 {
		t.Fatal("duplicate queue row")
	}
}

func TestRenderDeadlineSurvivesRestartAndQueueCancellation(t *testing.T) {
	app := newTestCtx(t)
	for i := 0; i < 2; i++ {
		_, err := insertRender(app.AppDB(), testProj, "crop", []string{"1"}, map[string]any{"overall_timeout_seconds": 3600}, fmt.Sprintf("%d.png", i), "/", "")
		if err != nil {
			t.Fatal(err)
		}
	}
	row, err := claimNextPending(app.AppDB())
	if err != nil {
		t.Fatal(err)
	}
	before, err := renderOverallDeadline(app, row)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = recoverInterruptedRenders(app.AppDB()); err != nil {
		t.Fatal(err)
	}
	recovered, err := claimNextPending(app.AppDB())
	if err != nil || recovered.ID != row.ID {
		t.Fatalf("recovery changed identity/order: %+v %v", recovered, err)
	}
	after, err := renderOverallDeadline(app, recovered)
	if err != nil || !before.Equal(after) {
		t.Fatal("restart renewed deadline")
	}
	if err = renderMarkCancelled(app.AppDB(), row.ID); err != nil {
		t.Fatal(err)
	}
	next, err := claimNextPending(app.AppDB())
	if err != nil || next.ID == row.ID {
		t.Fatal("cancelled render was recovered")
	}
	for _, seconds := range []any{0, 29, 604801, 1.5, "300"} {
		if _, err := insertRender(app.AppDB(), testProj, "crop", []string{"1"}, map[string]any{"overall_timeout_seconds": seconds}, "bad.png", "/", ""); err == nil {
			t.Fatalf("accepted invalid deadline: %v", seconds)
		}
	}
}

func TestRenderShutdownDefersOnlyWaitingWork(t *testing.T) {
	app := newTestCtx(t)
	_, _ = insertRender(app.AppDB(), testProj, "crop", []string{"1"}, nil, "out.png", "/", "")
	row, _ := claimNextPending(app.AppDB())
	before, _ := renderOverallDeadline(app, row)
	// Keep the closed lifecycle available, as it is during worker shutdown.
	l := &mediaLifecycle{done: make(chan struct{})}
	close(l.done)
	mediaLifecycles.Store(app.AppDB(), l)
	defer mediaLifecycles.Delete(app.AppDB())
	if !deferWaitingRenderOnShutdown(app, row) {
		t.Fatal("waiting render was lost on shutdown")
	}
	row, _ = claimNextPending(app.AppDB())
	after, _ := renderOverallDeadline(app, row)
	if !before.Equal(after) {
		t.Fatal("shutdown renewed deadline")
	}
	recordRenderMetric(app, row, "execution_started", true)
	if deferWaitingRenderOnShutdown(app, row) {
		t.Fatal("executing render was blindly requeued")
	}
}

func TestRenderWaitingWorkerShutdownKeepsRequest(t *testing.T) {
	app := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj), tk.WithConfig(map[string]string{"execution_mode": "component", "media_work_capacity": "2"}))
	initMediaLifecycle(app)
	_, release, err := acquireMediaWork(context.Background(), app, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	id, err := insertRender(app.AppDB(), testProj, "crop", []string{"999"}, nil, "out.png", "/", "")
	if err != nil {
		t.Fatal(err)
	}
	row, err := claimNextPending(app.AppDB())
	if err != nil {
		t.Fatal(err)
	}
	before, err := renderOverallDeadline(app, row)
	if err != nil {
		t.Fatal(err)
	}
	startMediaWorker(app, func() { runOneRender(app, row, nil, nil, 30) })
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		var metrics map[string]any
		_ = json.Unmarshal(renderMetrics(app.AppDB(), id), &metrics)
		if metrics["admission_queue_position"] == float64(1) {
			break
		}
		time.Sleep(time.Millisecond)
	}
	stopped := make(chan struct{})
	go func() { stopMediaLifecycle(app.AppDB()); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(3 * time.Second):
		t.Fatal("queue shutdown retained a worker/lease")
	}
	got, err := getRender(app.AppDB(), testProj, id)
	if err != nil || got.Status != "pending" {
		t.Fatalf("request lost on shutdown: %+v %v", got, err)
	}
	initMediaLifecycle(app)
	defer stopMediaLifecycle(app.AppDB())
	reclaimed, err := claimNextPending(app.AppDB())
	if err != nil || reclaimed.ID != id {
		t.Fatal("restart changed render identity")
	}
	after, err := renderOverallDeadline(app, reclaimed)
	if err != nil || !before.Equal(after) {
		t.Fatal("restart renewed deadline")
	}
}
