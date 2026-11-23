package main

import "testing"

// Synced Gmail messages expose their thread id on message_get and
// message_list, so callers can find the Gmail thread without a provider call.
func TestMessageReadsExposeGmailThreadID(t *testing.T) {
	ctx := newTestCtx(t, gmailTestPlatform())
	app := &App{}
	if _, err := app.toolSendersCreate(ctx, map[string]any{"address": "support@example.com", "connection_id": int64(3)}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ { // first tick sets the cursor, second imports history
		if err := app.syncGmailMailboxes(ctx); err != nil {
			t.Fatal(err)
		}
	}
	var id int64
	if err := ctx.AppDB().QueryRow(`SELECT id FROM messages WHERE project_id='test-proj' AND provider_message_id='gmail-in-1'`).Scan(&id); err != nil {
		t.Fatal(err)
	}

	got, err := app.toolMessageGet(ctx, map[string]any{"id": float64(id)})
	if err != nil {
		t.Fatal(err)
	}
	if m := got.(map[string]any)["message"].(*Message); m.ProviderThreadID != "gmail-thread-in" {
		t.Fatalf("message_get provider_thread_id=%q", m.ProviderThreadID)
	}

	list, err := app.toolMessageList(ctx, map[string]any{"direction": "in"})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, m := range list.(map[string]any)["messages"].([]*Message) {
		if m.ID == id {
			found = m.ProviderThreadID == "gmail-thread-in"
		}
	}
	if !found {
		t.Fatal("message_list did not return provider_thread_id")
	}
}
