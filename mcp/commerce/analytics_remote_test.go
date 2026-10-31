package main

// Optional marked integration check. Only Analytics receives QA data; no
// Commerce/Billing records or payment requests are sent to the remote system.
import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

type remoteAnalyticsProbe struct {
	sdk.PlatformClient
	ids []float64
}

func (p *remoteAnalyticsProbe) CallAppResult(app, tool string, input map[string]any, out any) error {
	if app != "analytics" {
		return fmt.Errorf("QA forbids remote app %s", app)
	}
	args := copyMap(input)
	args["_project_id"] = os.Getenv("PIXEL_ANALYTICS_QA_PROJECT")
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": tool, "arguments": args}})
	req, e := http.NewRequest("POST", os.Getenv("PIXEL_ANALYTICS_QA_BASE")+"/api/apps/analytics/mcp?install_id=49&project_id="+os.Getenv("PIXEL_ANALYTICS_QA_PROJECT"), bytes.NewReader(body))
	if e != nil {
		return e
	}
	req.Header.Set("Authorization", "Bearer "+os.Getenv("PIXEL_ANALYTICS_QA_TOKEN"))
	req.Header.Set("Content-Type", "application/json")
	resp, e := http.DefaultClient.Do(req)
	if e != nil {
		return e
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return fmt.Errorf("Analytics QA HTTP%d", resp.StatusCode)
	}
	var wire struct {
		Error  any `json:"error"`
		Result struct {
			IsError bool `json:"isError"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	if e = json.Unmarshal(data, &wire); e != nil {
		return e
	}
	if wire.Error != nil || wire.Result.IsError {
		return fmt.Errorf("Analytics QA rejected %s: %s", tool, string(data))
	}
	for _, c := range wire.Result.Content {
		if c.Type == "text" {
			if e = json.Unmarshal([]byte(c.Text), out); e != nil {
				return e
			}
			if tool == "analytics_track" {
				var r map[string]any
				json.Unmarshal([]byte(c.Text), &r)
				if id, ok := r["id"].(float64); ok {
					p.ids = append(p.ids, id)
				}
			}
			return nil
		}
	}
	return fmt.Errorf("Analytics QA response missing text")
}
func TestMarkedPaidWorkerAgainstRemoteAnalytics(t *testing.T) {
	if os.Getenv("PIXEL_ANALYTICS_QA_TOKEN") == "" {
		t.Skip("explicit marked Analytics-only remote verification")
	}
	p := &remoteAnalyticsProbe{}
	pid := os.Getenv("PIXEL_ANALYTICS_QA_PROJECT")
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(pid), tk.WithPlatform(p))
	dbStoreCreate(ctx.AppDB(), pid, map[string]any{"slug": "local-only-placeholder", "name": "Local fixture"})
	store, _, _, cart := seedCommerceCart(t, ctx.AppDB(), pid, "pixel-retold", 0)
	if store.ID != 2 {
		t.Fatal("fixture store must match scoped QA store")
	}
	_, e := ctx.AppDB().Exec(`UPDATE commerce_carts SET id=18 WHERE id=?`, cart.ID)
	if e != nil {
		t.Fatal(e)
	}
	meta := map[string]any{"analytics": map[string]any{"enabled": true, "enabled_at": "2000-01-01T00:00:00Z"}}
	_, e = ctx.AppDB().Exec(`UPDATE commerce_stores SET metadata_json=? WHERE id=2`, jsonText(meta, "{}"))
	if e != nil {
		t.Fatal(e)
	}
	attr := map[string]any{"analytics_attribution": map[string]any{"consent": "granted", "visitor_id": os.Getenv("PIXEL_ANALYTICS_QA_VISITOR"), "test_submission": true, "qa_run": "pr-qa-remote-paid-fixture-20261031"}}
	_, e = ctx.AppDB().Exec(`UPDATE commerce_carts SET metadata_json=? WHERE id=18`, jsonText(attr, "{}"))
	if e != nil {
		t.Fatal(e)
	}
	_, e = ctx.AppDB().Exec(`INSERT INTO commerce_sales(id,project_id,store_id,cart_id,invoice_id,payment_status,status,total_cents,currency,paid_at) VALUES(999000018,?,2,18,999000018,'paid','paid',4475,'USD',?)`, pid, os.Getenv("PIXEL_ANALYTICS_QA_PAID_AT"))
	if e != nil {
		t.Fatal(e)
	}
	if e = (&App{}).reconcileSaleAnalytics(context.Background(), ctx); e != nil {
		t.Fatal(e)
	}
	var delivered *string
	var lastError string
	ctx.AppDB().QueryRow(`SELECT delivered_at,last_error FROM commerce_analytics_deliveries WHERE sale_id=999000018`).Scan(&delivered, &lastError)
	if delivered == nil {
		t.Fatal("remote delivery pending", lastError)
	}
	ctx.AppDB().Exec(`UPDATE commerce_analytics_deliveries SET delivered_at=NULL,next_attempt_at=datetime('now','-1 second') WHERE sale_id=999000018`)
	if e = (&App{}).reconcileSaleAnalytics(context.Background(), ctx); e != nil {
		t.Fatal(e)
	}
	if len(p.ids) != 2 || p.ids[0] != p.ids[1] {
		t.Fatal("remote retry duplicated paid QA event", p.ids)
	}
	t.Logf("Exact paid worker delivered one marked QA Analytics event (id %.0f) and reused it on retry; remote financial systems were never called", p.ids[0])
}
