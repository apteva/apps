package main

import "testing"

func TestResolveAudienceCountsReasonsPaginationAndHealthyAlternate(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	list, err := dbListCreate(ctx.AppDB(), "test-proj", &List{Name: "Campaign audience"})
	if err != nil {
		t.Fatal(err)
	}
	create := func(email string, extra ...map[string]any) *Contact {
		channels := []any{}
		if email != "" {
			channels = append(channels, map[string]any{"kind": "email", "value": email, "is_primary": true})
		}
		for _, ch := range extra {
			channels = append(channels, ch)
		}
		contact := mustCreate(t, ctx, map[string]any{"display_name": email, "channels": channels})
		if err := dbListAddContact(ctx.AppDB(), "test-proj", list.ID, contact.ID, "test"); err != nil {
			t.Fatal(err)
		}
		return contact
	}

	healthy := create("healthy@example.test")
	alternate := create("bad-primary@example.test", map[string]any{
		"kind": "email", "value": "healthy-alternate@example.test", "is_primary": false,
	})
	unsubscribed := create("unsubscribed@example.test")
	quarantined := create("quarantined@example.test")
	noEmail := create("")
	automated := create("robot@example.test")
	_ = healthy
	_ = noEmail

	if _, err := ctx.AppDB().Exec(`UPDATE contact_channel_delivery_state SET status = 'hard_bounced'
		WHERE project_id = ? AND channel_id = ? AND transport = 'email'`, "test-proj", alternate.Channels[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := ctx.AppDB().Exec(`UPDATE contact_channel_delivery_state SET status = 'unsubscribed'
		WHERE project_id = ? AND channel_id = ? AND transport = 'email'`, "test-proj", unsubscribed.Channels[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := ctx.AppDB().Exec(`UPDATE contact_channel_delivery_state SET status = 'soft_bounced', quarantined = 1
		WHERE project_id = ? AND channel_id = ? AND transport = 'email'`, "test-proj", quarantined.Channels[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := dbAddTag(ctx.AppDB(), "test-proj", automated.ID, tagAutomated); err != nil {
		t.Fatal(err)
	}

	args := map[string]any{"channel": "email", "list_id": list.ID, "limit": 2}
	seenRecipients := map[int64]string{}
	seenExclusions := map[int64]string{}
	var first *AudienceResolution
	for {
		out, err := app.toolResolveAudience(ctx, args)
		if err != nil {
			t.Fatal(err)
		}
		page := out.(*AudienceResolution)
		if first == nil {
			first = page
		}
		for _, recipient := range page.Recipients {
			seenRecipients[recipient.ContactID] = recipient.Address
		}
		for _, exclusion := range page.Exclusions {
			seenExclusions[exclusion.ContactID] = exclusion.Reason
		}
		if !page.HasMore {
			break
		}
		args["after_contact_id"] = page.NextAfterContactID
	}

	if first.RawCount != 6 || first.EligibleCount != 2 || first.ExcludedCount != 4 {
		t.Fatalf("counts raw/eligible/excluded=%d/%d/%d, want 6/2/4", first.RawCount, first.EligibleCount, first.ExcludedCount)
	}
	for reason, want := range map[string]int64{"unsubscribed": 1, "quarantined": 1, "no_channel": 1, "automated": 1} {
		if got := first.ExcludedByReason[reason]; got != want {
			t.Errorf("excluded_by_reason[%s]=%d, want %d", reason, got, want)
		}
	}
	if got := seenRecipients[alternate.ID]; got != "healthy-alternate@example.test" {
		t.Errorf("alternate address=%q, want healthy alternate", got)
	}
	if seenExclusions[unsubscribed.ID] != "unsubscribed" || seenExclusions[quarantined.ID] != "quarantined" {
		t.Errorf("exclusions=%v", seenExclusions)
	}
	if len(seenRecipients) != 2 || len(seenExclusions) != 4 {
		t.Fatalf("paged results recipients=%d exclusions=%d, want 2/4", len(seenRecipients), len(seenExclusions))
	}
}

func TestResolveAudienceSupportsSingleContactAndTransportSpecificHealth(t *testing.T) {
	ctx := newTestCtx(t)
	contact := mustCreate(t, ctx, map[string]any{
		"channels": []any{map[string]any{"kind": "phone", "value": "+15551230000", "is_primary": true}},
	})
	if _, err := ctx.AppDB().Exec(`UPDATE contact_channel_delivery_state SET status = 'unsubscribed'
		WHERE project_id = ? AND channel_id = ? AND transport = 'sms'`, "test-proj", contact.Channels[0].ID); err != nil {
		t.Fatal(err)
	}
	app := &App{}
	smsOut, err := app.toolResolveAudience(ctx, map[string]any{"channel": "sms", "contact_id": contact.ID})
	if err != nil {
		t.Fatal(err)
	}
	sms := smsOut.(*AudienceResolution)
	if sms.EligibleCount != 0 || sms.ExcludedByReason["unsubscribed"] != 1 {
		t.Fatalf("sms result=%+v", sms)
	}
	waOut, err := app.toolResolveAudience(ctx, map[string]any{"channel": "whatsapp", "contact_id": contact.ID})
	if err != nil {
		t.Fatal(err)
	}
	wa := waOut.(*AudienceResolution)
	if wa.EligibleCount != 1 || len(wa.Recipients) != 1 || wa.Recipients[0].Address != "+15551230000" {
		t.Fatalf("whatsapp result=%+v", wa)
	}
}
