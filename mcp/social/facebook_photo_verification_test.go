package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const (
	testFacebookPhotoID  = "122100000000000010"
	testFacebookPagePost = `{"id":"page-1_42","from":{"id":"page-1"},"message":"Café",
		"created_time":"2026-10-27T18:01:00+0000","permalink_url":"https://www.facebook.com/page-1/posts/42",
		"is_published":true,"is_hidden":false,"attachments":{"data":[{"target":{"id":"122100000000000010"},"type":"photo"}]}}`
)

func TestFacebookLegacyPhotoPagePostInspection(t *testing.T) {
	const photoCreatedAt = "2026-10-27T18:00:00Z"
	hidden := strings.Replace(testFacebookPagePost, `"is_hidden":false`, `"is_hidden":true`, 1)
	unpublished := strings.Replace(testFacebookPagePost, `"is_published":true`, `"is_published":false`, 1)
	other := strings.Replace(hidden, "page-1_42", "page-1_43", 1)
	paging := `,"paging":{"next":"https://graph.facebook.com/posts?after=next","cursors":{"after":"next"}}`
	tests := []struct {
		name   string
		pages  []*sdk.ExecuteResult
		status string
		reason string
		flags  string
	}{
		{name: "published", pages: []*sdk.ExecuteResult{facebookTestPage(testFacebookPagePost, "")}, status: "verified", flags: "true,false"},
		{name: "hidden", pages: []*sdk.ExecuteResult{facebookTestPage(hidden, "")}, status: "verified", flags: "true,true"},
		{name: "unpublished", pages: []*sdk.ExecuteResult{facebookTestPage(unpublished, "")}, status: "verified", flags: "false,false"},
		{name: "wrong Page", pages: []*sdk.ExecuteResult{facebookTestPage(strings.Replace(testFacebookPagePost, `"from":{"id":"page-1"}`, `"from":{"id":"page-2"}`, 1), "")}, reason: "wrong or unknown Page"},
		{name: "ambiguous across pages", pages: []*sdk.ExecuteResult{facebookTestPage(testFacebookPagePost, paging), facebookTestPage(other, "")}, reason: "Multiple"},
		{name: "same post across pages", pages: []*sdk.ExecuteResult{facebookTestPage(testFacebookPagePost, paging), facebookTestPage(testFacebookPagePost, "")}, status: "verified", flags: "true,false"},
		{name: "missing flag", pages: []*sdk.ExecuteResult{facebookTestPage(strings.Replace(testFacebookPagePost, `"is_hidden":false,`, "", 1), "")}, reason: "omitted", flags: "true,null"},
		{name: "incomplete paging", pages: []*sdk.ExecuteResult{facebookTestPage(testFacebookPagePost, `,"paging":{"next":"https://graph.facebook.com/posts"}`)}, reason: "incomplete"},
		{name: "missing permission", pages: []*sdk.ExecuteResult{graphError(403, "Requires pages_read_engagement")}, reason: "pages_read_engagement"},
		{name: "different photo", pages: []*sdk.ExecuteResult{facebookTestPage(strings.Replace(testFacebookPagePost, testFacebookPhotoID, "778", 1), "")}, reason: "No Facebook Page post matched"},
		{name: "different caption", pages: []*sdk.ExecuteResult{facebookTestPage(strings.Replace(testFacebookPagePost, "Café", "changed caption", 1), "")}, reason: "caption"},
		{name: "outside window", pages: []*sdk.ExecuteResult{facebookTestPage(strings.Replace(testFacebookPagePost, "18:01:00", "19:01:00", 1), "")}, reason: "search window"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pf, ctx, account := facebookInspectFixture(t)
			managedFacebookTarget(t, ctx, account, testFacebookPhotoID, "")
			postID, targetID, attempts := facebookTestDelivery(t, ctx, testFacebookPhotoID, "")
			pf.executeResponses["facebook_get_post"] = graphOK(`{"id":"` + testFacebookPhotoID + `","from":{"id":"page-1"},"name":"Café","created_time":"` + photoCreatedAt + `"}`)
			pf.executeQueues["get_page_posts"] = tt.pages

			result, proof := inspectFacebookTestDelivery(t, ctx, account.ID, testFacebookPhotoID, postID, targetID)
			want := tt.status
			if want == "" {
				want = "unverified"
			}
			if proof.Status != want || !strings.Contains(proof.Reason, tt.reason) {
				t.Fatalf("Page post evidence = %+v, want %q with reason containing %q", proof, want, tt.reason)
			}
			if tt.flags != "" {
				published, _ := json.Marshal(proof.IsPublished)
				hidden, _ := json.Marshal(proof.IsHidden)
				if string(published)+","+string(hidden) != tt.flags {
					t.Fatalf("Page post flags = %s,%s, want %s", published, hidden, tt.flags)
				}
			}
			native := result["native_metadata"].(nativePost)
			if valueOrEmpty(native.CreatedAt) != photoCreatedAt {
				t.Fatalf("photo object creation time = %v", native.CreatedAt)
			}
			if want == "verified" && strings.HasPrefix(tt.flags, "true,") {
				if valueOrEmpty(proof.PagePostID) != "page-1_42" || valueOrEmpty(proof.PublishedAt) != "2026-10-27T18:01:00Z" ||
					valueOrEmpty(native.PublishedAt) != valueOrEmpty(proof.PublishedAt) || native.PublicationTimeSource != facebookPagePostTimeSource ||
					valueOrEmpty(proof.PermalinkURL) != "https://www.facebook.com/page-1/posts/42" {
					t.Fatalf("expected verified Page post publication, evidence=%+v native=%+v", proof, native)
				}
			} else if native.PublishedAt != nil || (tt.name == "unpublished" && proof.PublishedAt != nil) {
				t.Fatalf("photo creation was treated as publication, evidence=%+v native=%+v", proof, native)
			}
			if len(pf.executeCalls) != 1+len(tt.pages) {
				t.Fatalf("unexpected reads: %+v", pf.executeCalls)
			}
			for i, call := range pf.executeCalls[1:] {
				if call.Tool != "get_page_posts" || call.ConnID != account.ConnID ||
					call.Input["pageId"] != "page-1" || call.Input["access_token"] != "page-token" ||
					call.Input["include_hidden"] != true || call.Input["since"] == nil || call.Input["until"] == nil ||
					(i == 1 && call.Input["after"] != "next") {
					t.Fatalf("incorrect scoped Page post lookup: %+v", call)
				}
			}
			onlyReads(t, pf)
			afterPost, afterTarget, afterAttempts := facebookTestDelivery(t, ctx, testFacebookPhotoID, "")
			if afterPost != postID || afterTarget != targetID || afterAttempts != attempts {
				t.Fatal("inspection changed the original delivery")
			}
		})
	}
}

func TestFacebookNewPhotoPersistsBothIDsAndInspectsExistingTarget(t *testing.T) {
	pf, ctx, account := facebookInspectFixture(t)
	pf.callAppResponses["storage:files_get"] = json.RawMessage(`{"id":296,"content_type":"image/png","size_bytes":1308309}`)
	pf.callAppResponses["storage:files_get_url"] = json.RawMessage(`{"url":"https://agents.example.com/api/apps/storage/files/296/content/photo.png?sig=abc"}`)
	// Graph IDs can exceed the exact range of float64, even when represented as JSON numbers.
	pf.executeResponses["post_photo_to_page"] = graphOK(`{"data":{"id":` + testFacebookPhotoID + `,"post_id":"page-1_42"}}`)
	a := &App{}
	_, err := a.toolPostCreate(ctx, map[string]any{
		"mode": "publish", "body": "Café",
		"social_account_ids": []any{account.ID}, "media_storage_ids": []any{int64(296)},
	})
	if err != nil {
		t.Fatal(err)
	}
	postID, targetID, attempts := facebookTestDelivery(t, ctx, testFacebookPhotoID, "page-1_42")
	if attempts != 1 || len(pf.executeCalls) != 1 || pf.executeCalls[0].Tool != "post_photo_to_page" {
		t.Fatalf("expected one original photo publish, attempts=%d calls=%+v", attempts, pf.executeCalls)
	}

	pf.executeCalls = nil
	pf.executeResponses["facebook_get_post"] = graphOK(testFacebookPagePost)
	for _, id := range []string{testFacebookPhotoID, "page-1_42"} {
		_, proof := inspectFacebookTestDelivery(t, ctx, account.ID, id, postID, targetID)
		if proof.Status != "verified" || valueOrEmpty(proof.PagePostID) != "page-1_42" || valueOrEmpty(proof.PublishedAt) != "2026-10-27T18:01:00Z" {
			t.Fatalf("new photo evidence = %+v", proof)
		}
	}
	if len(pf.executeCalls) != 2 {
		t.Fatalf("expected one Page post read per inspection, got %+v", pf.executeCalls)
	}
	for _, call := range pf.executeCalls {
		if call.Tool != "facebook_get_post" || call.Input["postId"] != "page-1_42" ||
			call.Input["access_token"] != "page-token" || call.ConnID != account.ConnID {
			t.Fatalf("expected exact stored Page post read, got %+v", call)
		}
	}
	onlyReads(t, pf)

	cases := []struct {
		name      string
		response  *sdk.ExecuteResult
		reason    string
		published bool
		hidden    bool
	}{
		{name: "hidden", response: graphOK(strings.Replace(testFacebookPagePost, `"is_hidden":false`, `"is_hidden":true`, 1)), published: true, hidden: true},
		{name: "unpublished", response: graphOK(strings.Replace(testFacebookPagePost, `"is_published":true`, `"is_published":false`, 1))},
		{name: "wrong Page", response: graphOK(strings.Replace(testFacebookPagePost, `"from":{"id":"page-1"}`, `"from":{"id":"page-2"}`, 1)), reason: "ownership"},
		{name: "wrong photo", response: graphOK(strings.Replace(testFacebookPagePost, testFacebookPhotoID, "778", 1)), reason: "exact photo"},
		{name: "permission denied", response: graphError(403, "Requires pages_read_engagement"), reason: "pages_read_engagement"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			pf.executeResponses["facebook_get_post"] = tt.response
			_, proof := inspectFacebookTestDelivery(t, ctx, account.ID, testFacebookPhotoID, postID, targetID)
			if tt.reason != "" {
				if proof.Status != "unverified" || !strings.Contains(proof.Reason, tt.reason) {
					t.Fatalf("expected unverified evidence with %q, got %+v", tt.reason, proof)
				}
				return
			}
			if proof.Status != "verified" || proof.IsPublished == nil || *proof.IsPublished != tt.published ||
				proof.IsHidden == nil || *proof.IsHidden != tt.hidden || (proof.PublishedAt != nil) != tt.published {
				t.Fatalf("incorrect Page post evidence: %+v", proof)
			}
		})
	}
	onlyReads(t, pf)
	afterPost, afterTarget, afterAttempts := facebookTestDelivery(t, ctx, testFacebookPhotoID, "page-1_42")
	if afterPost != postID || afterTarget != targetID || afterAttempts != attempts {
		t.Fatal("inspection changed the original delivery")
	}
}

func facebookTestPage(posts, paging string) *sdk.ExecuteResult {
	return graphOK(`{"data":[` + posts + `]` + paging + `}`)
}

func inspectFacebookTestDelivery(t *testing.T, ctx *sdk.AppCtx, accountID int64, id string, postID, targetID int64) (map[string]any, *facebookPagePostEvidence) {
	t.Helper()
	out, err := (&App{}).toolPostNativeInspect(ctx, map[string]any{"social_account_id": accountID, "native_post_id": id})
	if err != nil {
		t.Fatal(err)
	}
	result := out.(map[string]any)
	proof, ok := result["facebook_page_post"].(*facebookPagePostEvidence)
	if !ok || result["post_id"] != postID || result["target_id"] != targetID ||
		result["public_live_verified"] != false || proof.PageID != "page-1" || proof.PhotoID != testFacebookPhotoID {
		t.Fatalf("inspection did not return Page post evidence for the original delivery: %+v", result)
	}
	if _, err := time.Parse(time.RFC3339Nano, proof.ObservedAt); err != nil {
		t.Fatalf("invalid evidence observation time %q", proof.ObservedAt)
	}
	return result, proof
}

func facebookTestDelivery(t *testing.T, ctx *sdk.AppCtx, wantPhoto, wantPagePost string) (postID, targetID int64, attempts int) {
	t.Helper()
	var posts, targets int
	var photo, pagePost, status string
	err := ctx.AppDB().QueryRow(`SELECT (SELECT count(*) FROM posts), (SELECT count(*) FROM post_targets),
		post_id, id, attempts, status, platform_post_id, COALESCE(facebook_page_post_id,'') FROM post_targets`).
		Scan(&posts, &targets, &postID, &targetID, &attempts, &status, &photo, &pagePost)
	if err != nil {
		t.Fatal(err)
	}
	if posts != 1 || targets != 1 || status != "published" || photo != wantPhoto || pagePost != wantPagePost {
		t.Fatalf("unexpected delivery state: posts=%d targets=%d status=%s photo=%q Page post=%q", posts, targets, status, photo, pagePost)
	}
	return postID, targetID, attempts
}
