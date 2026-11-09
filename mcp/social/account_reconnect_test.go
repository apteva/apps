package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

type reconnectTestPlatform struct {
	*recordingPlatform
	request sdk.OAuthReconnectRequest
	status  string
}

func (p *reconnectTestPlatform) StartOAuthReconnect(req sdk.OAuthReconnectRequest) (*sdk.OAuthReconnectResult, error) {
	p.request = req
	return &sdk.OAuthReconnectResult{ID: "receipt-1", Status: "pending", AuthorizeURL: "https://oauth.example/authorize", ConnectionID: req.ConnectionID}, nil
}

func (p *reconnectTestPlatform) OAuthReconnectStatus(req sdk.OAuthReconnectQuery) (*sdk.OAuthReconnectResult, error) {
	if req.Cancel {
		p.status = "cancelled"
	}
	return &sdk.OAuthReconnectResult{ID: req.ID, Status: p.status, ConnectionID: p.request.ConnectionID}, nil
}

func TestAccountReconnectPreservesBlackIron72AndInbox(t *testing.T) {
	const channel = "UCabcdefghijklmnopqrstuv"
	for _, tc := range []struct {
		name, remote, body, want string
		upstreamStatus           int
	}{
		{"empty successful read", "authorized", `{"items":[]}`, "comments_connected", 200},
		{"zero reported results", "authorized", `{"pageInfo":{"totalResults":0}}`, "comments_connected", 200},
		{"malformed success", "authorized", `{}`, "comments_unverified", 200},
		{"permission still missing", "authorized", `{"error":{"details":[{"reason":"ACCESS_TOKEN_SCOPE_INSUFFICIENT"}]}}`, "comments_unverified", 403},
		{"wrong channel", "wrong_account", "", "wrong_account", 0},
		{"cancelled", "pending", "", "cancelled", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &reconnectTestPlatform{recordingPlatform: newRecordingPlatform(), status: tc.remote}
			ctx := newSocialCtx(t, &reconnectLookupPlatform{reconnectTestPlatform: p})
			db := ctx.AppDB()
			if _, err := db.Exec(`INSERT INTO social_accounts(id,project_id,platform,connection_id,display_name,status,last_check_status)
				VALUES (72,'test-proj','youtube',130,'Black Iron','active','ok')`); err != nil {
				t.Fatal(err)
			}
			if err := rememberReconnectIdentity(db, 72, "test-proj", "youtube", channel); err != nil {
				t.Fatal(err)
			}
			// This is a legacy account with a saved channel binding and
			// an empty external_account_id.
			p.executeResponses["get_my_channel"] = &sdk.ExecuteResult{Success: true, Status: 200, Data: json.RawMessage(`{"items":[{"id":"` + channel + `"}]}`)}
			if _, err := db.Exec(`INSERT INTO inbox_items(id,project_id,social_account_id,platform,kind,external_id,body,occurred_at,status)
				VALUES(501,'test-proj',72,'youtube','comment','comment-1','Keep this',CURRENT_TIMESTAMP,'archived')`); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`INSERT INTO inbox_cursors(social_account_id,kind,cursor,last_error)
				VALUES(72,'poll_backoff','2099-01-01T00:00:00Z','ACCESS_TOKEN_SCOPE_INSUFFICIENT'),
				(72,'coverage','needs_reconnect','permission_required'),
				(72,?,'2026-10-10T00:00:00Z',NULL)`, youtubeHeadCursor); err != nil {
				t.Fatal(err)
			}
			p.executeResponses["list_channel_comment_threads"] = &sdk.ExecuteResult{
				Success: tc.upstreamStatus == 200, Status: tc.upstreamStatus, Data: json.RawMessage(tc.body),
			}
			app := &App{}
			accounts, err := app.toolAccountList(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			row := accounts.(map[string]any)["accounts"].([]map[string]any)[0]
			if row["last_check_status"] != "ok" || row["inbox_coverage"] != "needs_reconnect" || row["reconnect_supported"] != true {
				t.Fatalf("coverage and profile status must be independent: %+v", row)
			}
			start := httptest.NewRecorder()
			app.handleAccountsItem(start, httptest.NewRequest(http.MethodPost, "/accounts/72/reconnect", nil))
			var started socialReconnectResult
			if start.Code != 200 || json.Unmarshal(start.Body.Bytes(), &started) != nil || started.Status != "pending" || started.ConnectionID != 130 || started.SocialAccountID != 72 {
				t.Fatalf("start: HTTP %d %s", start.Code, start.Body.String())
			}
			if p.request.ProjectID != "test-proj" || !reflect.DeepEqual(p.request.ExpectedIdentities, []sdk.OAuthReconnectIdentity{{Kind: "youtube", ID: channel}}) {
				t.Fatalf("wrong reconnect request: %+v", p.request)
			}
			statusArgs := map[string]any{"social_account_id": 72, "reconnect_id": started.ID, "cancel": tc.name == "cancelled"}
			out, err := app.toolAccountReconnectStatus(ctx, statusArgs)
			if err != nil {
				t.Fatal(err)
			}
			got, ok := out.(socialReconnectResult)
			if !ok || got.Status != tc.want || got.ConnectionID != 130 || got.SocialAccountID != 72 || got.CommentsRead != (tc.want == "comments_connected") {
				t.Fatalf("status: %+v, want %s", out, tc.want)
			}
			if tc.want == "comments_connected" {
				calls := len(p.executeCalls)
				if _, err := app.toolAccountReconnectStatus(ctx, statusArgs); err != nil || len(p.executeCalls) != calls {
					t.Fatalf("completed reconnect should not run another sync, err=%v", err)
				}
			}
			var commentReads int
			for _, call := range p.executeCalls {
				if call.ConnID != 130 || (call.Tool != "get_my_channel" && call.Tool != "list_channel_comment_threads") {
					t.Fatalf("unexpected upstream request: %+v", call)
				}
				if call.Tool == "list_channel_comment_threads" {
					commentReads++
					if call.Input["allThreadsRelatedToChannelId"] != channel {
						t.Fatal("read targeted the wrong channel")
					}
				}
			}
			if (commentReads > 0) != (tc.remote == "authorized") {
				t.Fatalf("unexpected comment read count: %d", commentReads)
			}
			var id, conn, count int
			if err := db.QueryRow(`SELECT MIN(id), MIN(connection_id), COUNT(*) FROM social_accounts`).Scan(&id, &conn, &count); err != nil || id != 72 || conn != 130 || count != 1 {
				t.Fatalf("account changed: id=%d connection=%d count=%d err=%v", id, conn, count, err)
			}
			var body, status string
			if err := db.QueryRow(`SELECT body,status FROM inbox_items WHERE id=501 AND social_account_id=72`).Scan(&body, &status); err != nil || body != "Keep this" || status != "archived" {
				t.Fatalf("inbox history changed: body=%s status=%s err=%v", body, status, err)
			}
			if head, _ := youtubeCursor(ctx, 72, youtubeHeadCursor); head != "2026-10-10T00:00:00Z" {
				t.Fatalf("existing comment cursor changed: %s", head)
			}
			if backoff, _ := youtubeCursor(ctx, 72, "poll_backoff"); (backoff == "") != (tc.remote == "authorized") {
				t.Fatalf("permission backoff changed at the wrong time: %q", backoff)
			}
		})
	}
}

type reconnectLookupPlatform struct{ *reconnectTestPlatform }

func (p *reconnectLookupPlatform) GetConnection(id int64) (*sdk.PlatformConnection, error) {
	return &sdk.PlatformConnection{ID: id, AppSlug: "youtube-api", ProjectID: "test-proj", Status: "active"}, nil
}

func TestAccountReconnectVerifiesAllSharedMetaDestinations(t *testing.T) {
	p := &reconnectTestPlatform{recordingPlatform: newRecordingPlatform(), status: "pending"}
	ctx := newSocialCtx(t, p)
	if _, err := ctx.AppDB().Exec(`INSERT INTO social_accounts(id,project_id,platform,connection_id,external_account_id,display_name,status)
		VALUES (72,'test-proj','facebook',130,'123','Page','active'),
		(73,'test-proj','instagram',130,'456','Instagram','active'),
		(74,'test-proj','facebook',130,'789','Disconnected','disconnected')`); err != nil {
		t.Fatal(err)
	}
	out, err := (&App{}).toolAccountReconnect(ctx, map[string]any{"social_account_id": 72})
	if err != nil || !reflect.DeepEqual(p.request.ExpectedIdentities, []sdk.OAuthReconnectIdentity{{Kind: "facebook", ID: "123"}, {Kind: "instagram", ID: "456"}}) {
		t.Fatalf("shared reconnect=%+v identities=%+v err=%v", out, p.request.ExpectedIdentities, err)
	}
	if _, err := ctx.AppDB().Exec(`UPDATE social_accounts SET provider_slug='zernio' WHERE id=72`); err != nil {
		t.Fatal(err)
	}
	out, err = (&App{}).toolAccountReconnect(ctx, map[string]any{"social_account_id": 72})
	if err != nil || !strings.Contains(string(mustReconnectJSON(t, out)), "Provider-managed accounts") {
		t.Fatalf("provider-managed account must use provider recovery: %+v %v", out, err)
	}
}

func mustReconnectJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
