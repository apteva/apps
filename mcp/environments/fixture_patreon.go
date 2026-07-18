package main

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

func initialPatreonState(spec WebFixtureSpec) map[string]any {
	seed := spec.Seed
	text := func(key, fallback string) string {
		if value, ok := seed[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
		return fallback
	}
	scenario := spec.Scenario
	if scenario == "" {
		scenario = "new-visitor"
	}
	signedIn := scenario != "signed-out"
	var membership any
	if scenario == "existing-member" {
		membership = map[string]any{"status": "active", "tier": "supporter", "tier_name": "Supporter", "price": 5.0}
	}
	return map[string]any{
		"viewer": map[string]any{"name": text("viewer_name", "Alex Morgan"), "signed_in": signedIn},
		"creator": map[string]any{
			"slug": text("creator_slug", "studio-north"), "name": text("creator_name", "Studio North"),
			"tagline": text("creator_tagline", "Independent films and stories from the edge of the map."),
			"members": 1842.0, "posts_count": 96.0,
			"tiers": []any{
				map[string]any{"id": "supporter", "name": "Supporter", "price": 5.0, "description": "Early updates, production notes, and the complete member archive."},
				map[string]any{"id": "backstage", "name": "Backstage", "price": 12.0, "description": "Everything in Supporter plus monthly behind-the-scenes videos."},
				map[string]any{"id": "producer", "name": "Producer", "price": 30.0, "description": "Credits, quarterly group calls, and access to rough cuts."},
			},
		},
		"following":  scenario == "existing-member",
		"membership": membership,
		"checkout":   map[string]any{"selected_tier": "", "payment_should_fail": scenario == "payment-failure", "error": ""},
		"messages":   []any{},
		"members": []any{
			map[string]any{"id": "member-maya", "name": "Maya Chen", "tier": "Producer", "joined": "March 2024", "status": "active"},
			map[string]any{"id": "member-jordan", "name": "Jordan Lee", "tier": "Backstage", "joined": "September 2024", "status": "active"},
			map[string]any{"id": "member-sam", "name": "Sam Rivera", "tier": "Supporter", "joined": "January 2025", "status": "active"},
		},
		"member_threads": map[string]any{
			"member-maya": []any{
				map[string]any{"from": "member", "body": "Will the new short include a director commentary?", "sent_at": "Yesterday, 16:42"},
			},
			"member-jordan": []any{
				map[string]any{"from": "member", "body": "The observatory location looks incredible.", "sent_at": "Monday, 09:18"},
			},
			"member-sam": []any{},
		},
		"payouts": map[string]any{
			"currency": "USD", "available": 1248.75, "pending": 0.0, "lifetime": 32480.50,
			"method": map[string]any{"type": "bank", "label": "Bank account ending in 1842", "last_four": "1842", "status": "verified"},
			"history": []any{
				map[string]any{"id": "payout-1", "amount": 1184.20, "status": "paid", "date": "July 5"},
				map[string]any{"id": "payout-2", "amount": 1096.40, "status": "paid", "date": "June 5"},
			},
		},
		"posts": []any{
			map[string]any{"id": "post-1", "title": "Location notes: the old observatory", "date": "July 16", "excerpt": "A first look at the place where our next short film begins.", "body": "A first look at the place where our next short film begins.", "video_url": "", "status": "published", "audience": "public", "locked": false},
			map[string]any{"id": "post-2", "title": "Members: rough cut and director commentary", "date": "July 11", "excerpt": "Watch the new twelve-minute cut and leave your notes before picture lock.", "body": "Watch the new twelve-minute cut and leave your notes before picture lock.", "video_url": "https://vimeo.com/915402741", "status": "published", "audience": "members", "locked": membership == nil},
			map[string]any{"id": "post-3", "title": "June production journal", "date": "June 28", "excerpt": "Casting updates, practical effects tests, and what comes next.", "body": "Casting updates, practical effects tests, and what comes next.", "video_url": "", "status": "published", "audience": "members", "locked": membership == nil},
		},
	}
}

func normalizePatreonState(spec WebFixtureSpec, state map[string]any) map[string]any {
	defaults := initialPatreonState(spec)
	for _, key := range []string{"members", "member_threads", "payouts", "messages", "checkout"} {
		if state[key] == nil {
			state[key] = defaults[key]
		}
	}
	defaultPosts, _ := defaults["posts"].([]any)
	posts, _ := state["posts"].([]any)
	for _, value := range posts {
		post, _ := value.(map[string]any)
		if post == nil {
			continue
		}
		if post["status"] == nil {
			post["status"] = "published"
		}
		var defaultPost map[string]any
		for _, candidate := range defaultPosts {
			item, _ := candidate.(map[string]any)
			if item["id"] == post["id"] {
				defaultPost = item
				break
			}
		}
		if post["audience"] == nil {
			if defaultPost != nil {
				post["audience"] = defaultPost["audience"]
			} else if locked, _ := post["locked"].(bool); locked {
				post["audience"] = "members"
			} else {
				post["audience"] = "public"
			}
		}
		if post["body"] == nil {
			post["body"] = post["excerpt"]
		}
		if post["video_url"] == nil {
			if defaultPost != nil {
				post["video_url"] = defaultPost["video_url"]
			} else {
				post["video_url"] = ""
			}
		}
		if post["scheduled_at"] == nil {
			post["scheduled_at"] = ""
		}
	}
	return state
}

func applyPatreonAction(state map[string]any, action string, input map[string]any) (string, map[string]any, error) {
	viewer, _ := state["viewer"].(map[string]any)
	creator, _ := state["creator"].(map[string]any)
	checkout, _ := state["checkout"].(map[string]any)
	signedIn, _ := viewer["signed_in"].(bool)
	requireSignIn := func() error {
		if !signedIn {
			return errors.New("sign in required")
		}
		return nil
	}
	switch strings.TrimSpace(action) {
	case "sign_in":
		viewer["signed_in"] = true
		return "session.signed_in", map[string]any{"viewer": viewer["name"]}, nil
	case "follow":
		if err := requireSignIn(); err != nil {
			return "", nil, err
		}
		state["following"] = true
		return "creator.followed", map[string]any{"creator": creator["slug"]}, nil
	case "select_tier":
		if err := requireSignIn(); err != nil {
			return "", nil, err
		}
		tierID, _ := input["tier"].(string)
		tier := findPatreonTier(creator, tierID)
		if tier == nil {
			return "", nil, errors.New("membership tier not found")
		}
		checkout["selected_tier"] = tierID
		checkout["error"] = ""
		return "checkout.started", map[string]any{"creator": creator["slug"], "tier": tierID}, nil
	case "checkout":
		if err := requireSignIn(); err != nil {
			return "", nil, err
		}
		tierID, _ := checkout["selected_tier"].(string)
		tier := findPatreonTier(creator, tierID)
		if tier == nil {
			return "", nil, errors.New("select a membership tier")
		}
		if failed, _ := checkout["payment_should_fail"].(bool); failed {
			checkout["error"] = "Your test payment was declined."
			return "payment.failed", map[string]any{"creator": creator["slug"], "tier": tierID, "reason": "declined"}, nil
		}
		checkout["error"] = ""
		state["following"] = true
		state["membership"] = map[string]any{"status": "active", "tier": tierID, "tier_name": tier["name"], "price": tier["price"]}
		unlockPatreonPosts(state)
		return "membership.created", map[string]any{"creator": creator["slug"], "tier": tierID, "price": tier["price"]}, nil
	case "cancel_membership":
		membership, _ := state["membership"].(map[string]any)
		if membership == nil || membership["status"] != "active" {
			return "", nil, errors.New("no active membership")
		}
		membership["status"] = "cancelled"
		return "membership.cancelled", map[string]any{"creator": creator["slug"], "tier": membership["tier"]}, nil
	case "send_message":
		if err := requireSignIn(); err != nil {
			return "", nil, err
		}
		message, _ := input["message"].(string)
		message = strings.TrimSpace(message)
		if message == "" {
			return "", nil, errors.New("message required")
		}
		messages, _ := state["messages"].([]any)
		state["messages"] = append(messages, map[string]any{"body": message, "status": "sent", "recipient": creator["name"]})
		return "message.sent", map[string]any{"creator": creator["slug"], "message": message}, nil
	case "create_post":
		title := inputText(input, "title")
		body := inputText(input, "body")
		videoURL := inputText(input, "video_url")
		if title == "" {
			return "", nil, errors.New("post title required")
		}
		if body == "" {
			return "", nil, errors.New("post body required")
		}
		if err := validateFixtureURL(videoURL); err != nil {
			return "", nil, fmt.Errorf("video URL: %w", err)
		}
		audience := inputText(input, "audience")
		if audience == "" {
			audience = "members"
		}
		if audience != "public" && audience != "members" && audience != "paid" {
			return "", nil, errors.New("audience must be public, members, or paid")
		}
		publishMode := inputText(input, "publish_mode")
		if publishMode == "" {
			publishMode = "now"
		}
		status := "published"
		date := "Just now"
		eventType := "post.published"
		scheduledAt := ""
		if publishMode == "schedule" {
			scheduledAt = inputText(input, "scheduled_at")
			if !validFixtureSchedule(scheduledAt) {
				return "", nil, errors.New("valid schedule date and time required")
			}
			status = "scheduled"
			date = "Scheduled for " + strings.ReplaceAll(scheduledAt, "T", " ")
			eventType = "post.scheduled"
		} else if publishMode != "now" {
			return "", nil, errors.New("publish_mode must be now or schedule")
		}
		posts, _ := state["posts"].([]any)
		post := map[string]any{
			"id": fmt.Sprintf("post-%d", len(posts)+1), "title": title, "body": body, "excerpt": body,
			"video_url": videoURL, "status": status, "scheduled_at": scheduledAt, "date": date,
			"audience": audience, "locked": audience != "public",
		}
		state["posts"] = append([]any{post}, posts...)
		if count, ok := creator["posts_count"].(float64); ok {
			creator["posts_count"] = count + 1
		}
		return eventType, map[string]any{"post_id": post["id"], "title": title, "video_url": videoURL, "audience": audience, "scheduled_at": scheduledAt}, nil
	case "request_payout":
		payouts, _ := state["payouts"].(map[string]any)
		method, _ := payouts["method"].(map[string]any)
		if method == nil || method["status"] != "verified" {
			return "", nil, errors.New("verified payout method required")
		}
		amount, ok := inputNumber(input, "amount")
		if !ok || amount <= 0 {
			return "", nil, errors.New("positive payout amount required")
		}
		available, _ := payouts["available"].(float64)
		if amount > available {
			return "", nil, errors.New("payout exceeds available balance")
		}
		pending, _ := payouts["pending"].(float64)
		payouts["available"] = available - amount
		payouts["pending"] = pending + amount
		history, _ := payouts["history"].([]any)
		payout := map[string]any{"id": fmt.Sprintf("payout-%d", len(history)+1), "amount": amount, "status": "pending", "date": "Today"}
		payouts["history"] = append([]any{payout}, history...)
		return "payout.requested", map[string]any{"payout_id": payout["id"], "amount": amount, "currency": payouts["currency"], "method": method["last_four"]}, nil
	case "set_payout_method":
		payouts, _ := state["payouts"].(map[string]any)
		methodType := inputText(input, "type")
		lastFour := inputText(input, "last_four")
		if methodType != "bank" && methodType != "paypal" {
			return "", nil, errors.New("payout method must be bank or paypal")
		}
		if len(lastFour) != 4 || strings.Trim(lastFour, "0123456789") != "" {
			return "", nil, errors.New("last_four must contain four digits")
		}
		label := "Bank account ending in " + lastFour
		if methodType == "paypal" {
			label = "PayPal account ending in " + lastFour
		}
		payouts["method"] = map[string]any{"type": methodType, "label": label, "last_four": lastFour, "status": "verified"}
		return "payout.method_updated", map[string]any{"type": methodType, "last_four": lastFour}, nil
	case "send_member_message":
		memberID := inputText(input, "member_id")
		message := inputText(input, "message")
		member := findPatreonMember(state, memberID)
		if member == nil {
			return "", nil, errors.New("member not found")
		}
		if message == "" {
			return "", nil, errors.New("message required")
		}
		threads, _ := state["member_threads"].(map[string]any)
		thread, _ := threads[memberID].([]any)
		threads[memberID] = append(thread, map[string]any{"from": "creator", "body": message, "sent_at": "Just now"})
		return "member_message.sent", map[string]any{"member_id": memberID, "member_name": member["name"], "message": message}, nil
	default:
		return "", nil, errors.New("unsupported action")
	}
}

func inputText(input map[string]any, key string) string {
	value, _ := input[key].(string)
	return strings.TrimSpace(value)
}

func inputNumber(input map[string]any, key string) (float64, bool) {
	switch value := input[key].(type) {
	case float64:
		return value, true
	case int:
		return float64(value), true
	default:
		return 0, false
	}
}

func validateFixtureURL(value string) error {
	if value == "" {
		return errors.New("required")
	}
	parsed, err := url.Parse(value)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return errors.New("must be an http or https URL")
	}
	return nil
}

func validFixtureSchedule(value string) bool {
	if _, err := time.Parse("2006-01-02T15:04", value); err == nil {
		return true
	}
	_, err := time.Parse(time.RFC3339, value)
	return err == nil
}

func findPatreonMember(state map[string]any, id string) map[string]any {
	members, _ := state["members"].([]any)
	for _, value := range members {
		member, _ := value.(map[string]any)
		if member["id"] == id {
			return member
		}
	}
	return nil
}

func findPatreonTier(creator map[string]any, id string) map[string]any {
	tiers, _ := creator["tiers"].([]any)
	for _, value := range tiers {
		tier, _ := value.(map[string]any)
		if tier["id"] == id {
			return tier
		}
	}
	return nil
}

func unlockPatreonPosts(state map[string]any) {
	posts, _ := state["posts"].([]any)
	for _, value := range posts {
		if post, ok := value.(map[string]any); ok {
			post["locked"] = false
		}
	}
}

const patreonFixtureHTML = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>Patreon</title>
<style>
:root{--ink:#0b1828;--muted:#657282;--line:#e4e8ed;--paper:#fff;--soft:#f5f7f9;--coral:#ff424d;--coral-dark:#db2632;--teal:#167d7f;--yellow:#f5c451}*{box-sizing:border-box}body{margin:0;background:var(--paper);color:var(--ink);font-family:Inter,ui-sans-serif,-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif;letter-spacing:0}button,input,textarea{font:inherit}button{cursor:pointer}.top{height:68px;border-bottom:1px solid var(--line);display:flex;align-items:center;gap:28px;padding:0 30px;position:sticky;top:0;background:#fff;z-index:10}.brand{font-weight:900;font-size:21px;color:var(--coral);border:0;background:none;padding:0}.search{max-width:440px;flex:1;background:var(--soft);border:1px solid transparent;border-radius:6px;padding:11px 14px;color:var(--ink)}.search:focus{outline:none;border-color:#aab4c0}.nav{display:flex;align-items:center;gap:18px;margin-left:auto}.link{border:0;background:none;color:var(--ink);font-weight:650}.avatar-sm,.avatar{display:grid;place-items:center;background:var(--teal);color:#fff;font-weight:800}.avatar-sm{width:36px;height:36px;border-radius:50%}.notice{height:32px;background:var(--ink);color:#fff;display:flex;align-items:center;justify-content:center;font-size:12px}.cover{height:250px;background:#17324c;position:relative;overflow:hidden}.cover:before,.cover:after{content:"";position:absolute;background:var(--yellow)}.cover:before{width:42%;height:36px;left:8%;top:64px;transform:rotate(-5deg)}.cover:after{width:30%;height:150px;right:12%;bottom:-40px;transform:rotate(12deg);background:var(--coral)}.creator{max-width:1180px;margin:0 auto;padding:0 28px 70px}.identity{display:flex;align-items:flex-end;justify-content:space-between;gap:30px;margin-top:-58px;position:relative}.avatar{width:132px;height:132px;border:6px solid #fff;border-radius:50%;font-size:38px}.identity-copy{flex:1;padding-bottom:9px}.identity h1{margin:0;font-size:32px}.identity p{margin:6px 0 0;color:var(--muted)}.actions{display:flex;gap:9px;padding-bottom:12px}.primary,.secondary{border-radius:5px;padding:11px 18px;font-weight:750;border:1px solid var(--ink)}.primary{background:var(--coral);border-color:var(--coral);color:#fff}.primary:hover{background:var(--coral-dark)}.secondary{background:#fff}.stats{display:flex;gap:28px;padding:24px 0;border-bottom:1px solid var(--line);color:var(--muted);font-size:14px}.stats b{color:var(--ink)}.layout{display:grid;grid-template-columns:minmax(0,1fr) 360px;gap:46px;padding-top:36px}.section-title{font-size:21px;margin:0 0 18px}.post{border-top:1px solid var(--line);padding:20px 0}.post:first-of-type{border-top:0;padding-top:0}.post time{font-size:12px;color:var(--muted)}.post h3{font-size:18px;margin:7px 0}.post p{margin:0;color:var(--muted);line-height:1.55}.locked{margin-top:10px;padding:9px 11px;background:var(--soft);border-left:3px solid var(--yellow);font-size:13px}.tier{border:1px solid var(--line);border-radius:7px;padding:20px;margin-bottom:13px;background:#fff}.tier h3{margin:0}.price{font-size:25px;font-weight:850;margin:9px 0}.tier p{color:var(--muted);font-size:14px;line-height:1.5;min-height:42px}.tier button{width:100%}.member-box{border:1px solid var(--teal);border-radius:7px;padding:20px;background:#f3fbfa}.member-box h3{margin:0 0 8px}.member-box p{color:var(--muted);font-size:14px}.messages{max-width:900px;margin:40px auto;padding:0 24px}.composer{border:1px solid var(--line);border-radius:7px;padding:20px}.composer textarea{width:100%;min-height:130px;border:1px solid var(--line);border-radius:5px;padding:12px;resize:vertical}.composer .row{display:flex;justify-content:flex-end;margin-top:12px}.sent{margin-top:15px;padding:14px;background:var(--soft);border-radius:5px}.modal-wrap{position:fixed;inset:0;background:rgba(11,24,40,.62);display:grid;place-items:center;padding:20px;z-index:20}.modal{width:min(520px,100%);background:#fff;border-radius:8px;box-shadow:0 24px 80px rgba(0,0,0,.3)}.modal-head{display:flex;justify-content:space-between;padding:20px 22px;border-bottom:1px solid var(--line)}.modal-head h2{margin:0;font-size:19px}.close{border:0;background:none;font-size:24px}.modal-body{padding:22px}.order{display:flex;justify-content:space-between;background:var(--soft);padding:14px;border-radius:5px;margin-bottom:18px}.fields{display:grid;grid-template-columns:1fr 120px;gap:12px}.fields label:first-child{grid-column:1/-1}.fields span{display:block;color:var(--muted);font-size:12px;margin-bottom:5px}.fields input{width:100%;border:1px solid var(--line);border-radius:5px;padding:10px}.error{margin:13px 0 0;color:#b4232d;background:#fff1f2;padding:10px;border-radius:5px;font-size:13px}.success{padding:32px;text-align:center}.success h2{margin-top:0}.auth{max-width:430px;margin:90px auto;border:1px solid var(--line);border-radius:7px;padding:30px;text-align:center}.auth h1{margin-top:0}.auth p{color:var(--muted);line-height:1.5}.hidden{display:none!important}@media(max-width:800px){.top{padding:0 16px;gap:14px}.search{display:none}.nav .link:first-child{display:none}.cover{height:170px}.creator{padding:0 18px 50px}.identity{align-items:center;margin-top:-45px;flex-wrap:wrap}.avatar{width:96px;height:96px;font-size:28px}.identity-copy{min-width:calc(100% - 126px)}.identity h1{font-size:25px}.actions{width:100%;padding:0}.actions button{flex:1}.layout{grid-template-columns:1fr;gap:28px}.fields{grid-template-columns:1fr}.fields label:first-child{grid-column:auto}}
</style>
<style>
.creator-nav{display:flex;align-items:center;gap:4px}.creator-nav .link{padding:8px 10px;border-radius:4px}.creator-nav .link:hover{background:var(--soft)}.workspace{max-width:1380px;margin:0 auto;padding:34px 30px 70px}.workspace-head{display:flex;align-items:center;justify-content:space-between;gap:20px;margin-bottom:26px}.workspace-head h1{margin:0;font-size:26px}.workspace-head p{margin:6px 0 0;color:var(--muted)}.dashboard-grid{display:grid;grid-template-columns:repeat(3,minmax(0,1fr));gap:14px;margin-bottom:28px}.metric{border:1px solid var(--line);border-radius:6px;padding:18px}.metric span{display:block;color:var(--muted);font-size:12px;margin-bottom:8px}.metric strong{font-size:25px}.quick-grid{display:grid;grid-template-columns:repeat(3,minmax(0,1fr));gap:14px}.quick{border:1px solid var(--line);border-radius:6px;padding:20px;text-align:left;background:#fff}.quick:hover{border-color:#9ba6b2;background:var(--soft)}.quick b,.quick span{display:block}.quick span{color:var(--muted);font-size:13px;margin-top:7px;line-height:1.45}.table{border:1px solid var(--line);border-radius:6px;overflow:hidden}.table-row{display:grid;grid-template-columns:minmax(0,1fr) 150px 150px 140px;gap:16px;align-items:center;padding:14px 16px;border-bottom:1px solid var(--line)}.table-row:last-child{border-bottom:0}.table-head{background:var(--soft);color:var(--muted);font-size:11px;font-weight:750;text-transform:uppercase}.status{font-size:12px;font-weight:750;text-transform:capitalize}.status.scheduled,.status.pending{color:#9a6500}.status.published,.status.paid,.status.active{color:var(--teal)}.editor-grid{display:grid;grid-template-columns:minmax(0,1fr) 340px;gap:28px;align-items:start}.editor-main,.publish-panel,.payout-panel,.chat-shell{border:1px solid var(--line);border-radius:6px;background:#fff}.editor-main{padding:24px}.publish-panel{padding:20px;position:sticky;top:90px}.field{display:block;margin-bottom:18px}.field>span{display:block;font-size:12px;font-weight:700;margin-bottom:7px}.field input,.field textarea,.field select,.amount-row input{width:100%;border:1px solid #cbd2da;border-radius:5px;padding:11px;background:#fff}.field textarea{min-height:220px;resize:vertical}.field .video{min-height:auto}.hint{color:var(--muted);font-size:12px;line-height:1.45;margin-top:6px}.radio-stack{display:grid;gap:9px;margin-bottom:18px}.radio-option{display:flex;gap:9px;align-items:flex-start;border:1px solid var(--line);border-radius:5px;padding:10px}.radio-option input{margin-top:2px}.publish-panel .primary{width:100%}.preview-video{padding:14px;border:1px dashed #aab4c0;border-radius:5px;color:var(--muted);font-size:13px}.payout-layout{display:grid;grid-template-columns:minmax(0,1fr) 380px;gap:24px}.payout-panel{padding:22px}.balance{font-size:38px;font-weight:850;margin:7px 0 20px}.amount-row{display:grid;grid-template-columns:1fr auto;gap:9px}.method{display:flex;justify-content:space-between;gap:16px;padding:15px;background:var(--soft);border-radius:5px;margin:16px 0}.member-layout{display:grid;grid-template-columns:300px minmax(0,1fr);min-height:560px}.member-list{border-right:1px solid var(--line);padding:10px}.member-item{display:block;width:100%;border:0;background:#fff;text-align:left;padding:13px;border-radius:5px}.member-item:hover,.member-item.active{background:var(--soft)}.member-item b,.member-item span{display:block}.member-item span{font-size:12px;color:var(--muted);margin-top:4px}.chat{display:flex;flex-direction:column;min-width:0}.chat-head{padding:18px 20px;border-bottom:1px solid var(--line)}.thread{flex:1;padding:20px;overflow:auto;background:#fafbfc}.bubble{max-width:72%;padding:11px 13px;border-radius:6px;background:#fff;border:1px solid var(--line);margin-bottom:12px}.bubble.creator{margin-left:auto;background:#eef8f7;border-color:#b7dedd}.bubble small{display:block;color:var(--muted);margin-top:6px}.chat-compose{display:grid;grid-template-columns:1fr auto;gap:10px;padding:16px;border-top:1px solid var(--line)}.chat-compose input{border:1px solid #cbd2da;border-radius:5px;padding:11px}.toast{position:fixed;right:22px;bottom:22px;background:var(--ink);color:#fff;padding:12px 16px;border-radius:5px;z-index:30}.video-link{display:inline-block;margin-top:9px;color:var(--teal);font-weight:700;font-size:13px}
@media(max-width:980px){.creator-nav .link:nth-child(3),.creator-nav .link:nth-child(4){display:none}.dashboard-grid,.quick-grid{grid-template-columns:1fr}.editor-grid,.payout-layout{grid-template-columns:1fr}.publish-panel{position:static}.member-layout{grid-template-columns:240px minmax(0,1fr)}}
@media(max-width:700px){.workspace{padding:24px 16px 50px}.workspace-head{align-items:flex-start;flex-direction:column}.creator-nav .link:not(:first-child){display:none}.table-row{grid-template-columns:minmax(0,1fr) 90px}.table-row>*:nth-child(3){display:none}.member-layout{grid-template-columns:1fr}.member-list{border-right:0;border-bottom:1px solid var(--line);display:flex;overflow:auto}.member-item{min-width:180px}.chat-shell{border-left:0;border-right:0}.amount-row{grid-template-columns:1fr}.bubble{max-width:88%}}
</style>
</head>
<body>
<div class="notice">Test environment - no real post, payment, payout, or message will be sent</div>
<header class="top"><button class="brand" data-nav="public">PATREON</button><input class="search" aria-label="Search creators" placeholder="Find a creator"><nav class="nav creator-nav"><button class="link" data-nav="dashboard">Creator dashboard</button><button class="link" data-nav="posts">Posts</button><button class="link" data-nav="members">Members</button><button class="link" data-nav="payouts">Payouts</button><button class="link" data-nav="public">View page</button><div class="avatar-sm" id="viewerInitial">A</div></nav></header>
<main id="app"></main>
<div id="modal"></div>
<script>
const match=location.pathname.match(/^(.*\/fixtures\/[^/]+\/[^/]+\/[^/]+\/)/);const base=match?match[1]:location.pathname;let state=null;let checkoutOpen=false;let completed=false;let selectedMember='member-maya';
const esc=v=>String(v??'').replace(/[&<>"']/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
async function load(){const r=await fetch(base+'api/state');const body=await r.json();state=body.state;render()}
async function act(action,input={}){const r=await fetch(base+'api/action',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({action,input})});const body=await r.json();if(!r.ok)throw new Error(body.error||'Action failed');state=body.state;render();return body}
function routeView(){const tail=location.pathname.slice(base.length);if(tail.startsWith('creator-dashboard/posts/new'))return'new-post';if(tail.startsWith('creator-dashboard/posts'))return'posts';if(tail.startsWith('creator-dashboard/members'))return'members';if(tail.startsWith('creator-dashboard/payouts'))return'payouts';if(tail.startsWith('creator-dashboard'))return'dashboard';if(tail.startsWith('messages'))return'messages';return'public'}
let view=routeView();
function go(next){const routes={public:'creator/'+state.creator.slug,messages:'messages',dashboard:'creator-dashboard',posts:'creator-dashboard/posts','new-post':'creator-dashboard/posts/new',members:'creator-dashboard/members',payouts:'creator-dashboard/payouts'};view=next;history.pushState({},'',base+routes[next]);render()}
document.addEventListener('click',e=>{const nav=e.target.closest('[data-nav]');if(nav)go(nav.dataset.nav)});window.addEventListener('popstate',()=>{view=routeView();render()});
function publicView(){const c=state.creator,m=state.membership;const initials=esc(c.name.split(/\s+/).map(x=>x[0]).slice(0,2).join(''));const posts=state.posts.filter(p=>p.status==='published').map(p=>'<article class="post"><time>'+esc(p.date)+'</time><h3>'+esc(p.title)+'</h3><p>'+esc(p.excerpt)+'</p>'+(p.video_url?'<span class="video-link">Video attached</span>':'')+(p.locked?'<div class="locked">Members-only post</div>':'')+'</article>').join('');const memberships=m&&m.status==='active'?'<div class="member-box"><h3>'+esc(m.tier_name)+' member</h3><p>Your membership is active at $'+Number(m.price).toFixed(0)+' per month.</p><button class="secondary" id="cancel">Cancel membership</button></div>':c.tiers.map(t=>'<article class="tier"><h3>'+esc(t.name)+'</h3><div class="price">$'+Number(t.price).toFixed(0)+' <small>/ month</small></div><p>'+esc(t.description)+'</p><button class="primary join" data-tier="'+esc(t.id)+'">Join</button></article>').join('');return '<section class="cover"></section><section class="creator"><div class="identity"><div class="avatar">'+initials+'</div><div class="identity-copy"><h1>'+esc(c.name)+'</h1><p>'+esc(c.tagline)+'</p></div><div class="actions"><button class="secondary" id="follow">'+(state.following?'Following':'Follow')+'</button><button class="primary" id="message">Message</button></div></div><div class="stats"><span><b>'+Number(c.members).toLocaleString()+'</b> members</span><span><b>'+Number(c.posts_count).toLocaleString()+'</b> posts</span></div><div class="layout"><section><h2 class="section-title">Latest posts</h2>'+posts+'</section><aside><h2 class="section-title">Choose your membership</h2>'+memberships+'</aside></div></section>'}
function messagesView(){const sent=state.messages.map(m=>'<div class="sent"><b>Sent to '+esc(m.recipient)+'</b><p>'+esc(m.body)+'</p></div>').join('');return '<section class="messages"><h1>Message '+esc(state.creator.name)+'</h1><div class="composer"><textarea id="messageBody" aria-label="Message" placeholder="Write a message"></textarea><div class="row"><button class="primary" id="send">Send message</button></div></div>'+sent+'</section>'}
function dashboardView(){const p=state.payouts;const scheduled=state.posts.filter(x=>x.status==='scheduled').length;return '<section class="workspace"><div class="workspace-head"><div><h1>Creator dashboard</h1><p>'+esc(state.creator.name)+'</p></div><button class="primary" data-nav="new-post">New post</button></div><div class="dashboard-grid"><div class="metric"><span>Active members</span><strong>'+Number(state.creator.members).toLocaleString()+'</strong></div><div class="metric"><span>Available payout</span><strong>$'+Number(p.available).toFixed(2)+'</strong></div><div class="metric"><span>Scheduled posts</span><strong>'+scheduled+'</strong></div></div><h2 class="section-title">Manage your creator business</h2><div class="quick-grid"><button class="quick" data-nav="new-post"><b>Create a post</b><span>Add a video and publish now or schedule it.</span></button><button class="quick" data-nav="members"><b>Chat with members</b><span>Open member conversations and reply directly.</span></button><button class="quick" data-nav="payouts"><b>Manage payouts</b><span>Review earnings, payout method, and transfers.</span></button></div></section>'}
function postsView(){const rows=state.posts.map(p=>'<div class="table-row"><div><b>'+esc(p.title)+'</b>'+(p.video_url?'<span class="video-link">'+esc(p.video_url)+'</span>':'')+'</div><span class="status '+esc(p.status)+'">'+esc(p.status)+'</span><span>'+esc(p.audience||'members')+'</span><span>'+esc(p.scheduled_at||p.date)+'</span></div>').join('');return '<section class="workspace"><div class="workspace-head"><div><h1>Posts</h1><p>Published and scheduled creator posts</p></div><button class="primary" data-nav="new-post">New post</button></div><div class="table"><div class="table-row table-head"><span>Post</span><span>Status</span><span>Audience</span><span>Date</span></div>'+rows+'</div></section>'}
function newPostView(){return '<section class="workspace"><div class="workspace-head"><div><h1>New post</h1><p>Create a video post for your audience</p></div><button class="secondary" data-nav="posts">Cancel</button></div><div class="editor-grid"><div class="editor-main"><label class="field"><span>Title</span><input id="postTitle" aria-label="Post title" placeholder="Post title"></label><label class="field"><span>Post</span><textarea id="postBody" aria-label="Post body" placeholder="Write your post"></textarea></label><label class="field"><span>Video link</span><input class="video" id="videoUrl" type="url" aria-label="Video link" placeholder="https://vimeo.com/..."><div class="hint">YouTube, Vimeo, or another HTTPS video URL</div></label><div class="preview-video" id="videoPreview">No video link added</div></div><aside class="publish-panel"><h2 class="section-title">Publish</h2><label class="field"><span>Audience</span><select id="postAudience" aria-label="Audience"><option value="members">All members</option><option value="paid">Paid members only</option><option value="public">Public</option></select></label><span class="field"><span>Timing</span></span><div class="radio-stack"><label class="radio-option"><input type="radio" name="publishMode" value="now" checked><span><b>Publish now</b></span></label><label class="radio-option"><input type="radio" name="publishMode" value="schedule"><span><b>Schedule</b></span></label></div><label class="field hidden" id="scheduleField"><span>Date and time</span><input id="scheduledAt" type="datetime-local" aria-label="Schedule date and time"></label><button class="primary" id="publishPost">Publish post</button></aside></div></section>'}
function payoutsView(){const p=state.payouts,m=p.method;const rows=p.history.map(x=>'<div class="table-row"><b>'+esc(x.id)+'</b><span class="status '+esc(x.status)+'">'+esc(x.status)+'</span><span>$'+Number(x.amount).toFixed(2)+'</span><span>'+esc(x.date)+'</span></div>').join('');return '<section class="workspace"><div class="workspace-head"><div><h1>Payouts</h1><p>Earnings and transfer history</p></div></div><div class="payout-layout"><div><div class="dashboard-grid"><div class="metric"><span>Available</span><strong>$'+Number(p.available).toFixed(2)+'</strong></div><div class="metric"><span>Pending</span><strong>$'+Number(p.pending).toFixed(2)+'</strong></div><div class="metric"><span>Lifetime</span><strong>$'+Number(p.lifetime).toFixed(2)+'</strong></div></div><div class="table"><div class="table-row table-head"><span>Payout</span><span>Status</span><span>Amount</span><span>Date</span></div>'+rows+'</div></div><aside class="payout-panel"><span class="hint">Available to withdraw</span><div class="balance">$'+Number(p.available).toFixed(2)+'</div><label class="field"><span>Amount</span><input id="payoutAmount" type="number" min="0.01" step="0.01" value="'+Number(p.available).toFixed(2)+'" aria-label="Payout amount"></label><div class="method"><div><b>'+esc(m.label)+'</b><div class="hint">'+esc(m.status)+'</div></div><span>'+esc(m.type)+'</span></div><button class="primary" id="requestPayout" style="width:100%">Request payout</button><hr style="border:0;border-top:1px solid var(--line);margin:22px 0"><label class="field"><span>Payout method</span><select id="methodType"><option value="bank" '+(m.type==='bank'?'selected':'')+'>Bank account</option><option value="paypal" '+(m.type==='paypal'?'selected':'')+'>PayPal</option></select></label><label class="field"><span>Last four</span><input id="methodLastFour" maxlength="4" value="'+esc(m.last_four)+'"></label><button class="secondary" id="saveMethod" style="width:100%">Update method</button></aside></div></section>'}
function membersView(){const members=state.members;const current=members.find(x=>x.id===selectedMember)||members[0];selectedMember=current.id;const list=members.map(m=>'<button class="member-item '+(m.id===current.id?'active':'')+'" data-member="'+esc(m.id)+'"><b>'+esc(m.name)+'</b><span>'+esc(m.tier)+' - '+esc(m.status)+'</span></button>').join('');const thread=(state.member_threads[current.id]||[]).map(m=>'<div class="bubble '+(m.from==='creator'?'creator':'')+'"><span>'+esc(m.body)+'</span><small>'+(m.from==='creator'?'You':esc(current.name))+' - '+esc(m.sent_at)+'</small></div>').join('');return '<section class="workspace"><div class="workspace-head"><div><h1>Members</h1><p>Direct member conversations</p></div></div><div class="chat-shell member-layout"><aside class="member-list">'+list+'</aside><div class="chat"><div class="chat-head"><b>'+esc(current.name)+'</b><div class="hint">'+esc(current.tier)+' member since '+esc(current.joined)+'</div></div><div class="thread">'+(thread||'<p class="hint">No messages yet</p>')+'</div><div class="chat-compose"><input id="memberMessage" aria-label="Message member" placeholder="Message '+esc(current.name)+'"><button class="primary" id="sendMemberMessage">Send</button></div></div></div></section>'}
function authView(){return '<section class="auth"><h1>Welcome back</h1><p>Sign in to follow creators, join memberships, and send messages.</p><button class="primary" id="signIn">Continue as '+esc(state.viewer.name)+'</button></section>'}
function modal(){if(!checkoutOpen)return'';const tier=state.creator.tiers.find(t=>t.id===state.checkout.selected_tier);if(completed)return '<div class="modal-wrap"><div class="modal success"><h2>Welcome to '+esc(tier.name)+'</h2><p>Your membership is active. Member posts are now available.</p><button class="primary" id="done">Done</button></div></div>';const error=state.checkout.error?'<p class="error">'+esc(state.checkout.error)+'</p>':'';return '<div class="modal-wrap"><div class="modal"><div class="modal-head"><h2>Complete your membership</h2><button class="close" id="close" aria-label="Close">×</button></div><div class="modal-body"><div class="order"><span>'+esc(tier.name)+'</span><b>$'+Number(tier.price).toFixed(0)+' / month</b></div><div class="fields"><label><span>Card number</span><input value="4242 4242 4242 4242" aria-label="Card number"></label><label><span>Expiry</span><input value="12/30" aria-label="Expiry"></label><label><span>CVC</span><input value="123" aria-label="CVC"></label></div>'+error+'<button class="primary" id="confirm" style="width:100%;margin-top:18px">Confirm membership</button></div></div></div>'}
function toast(message){document.querySelector('.toast')?.remove();const node=document.createElement('div');node.className='toast';node.textContent=message;document.body.appendChild(node);setTimeout(()=>node.remove(),2600)}
async function attempt(fn,success){try{await fn();if(success)toast(success)}catch(e){toast(e.message)}}
function bind(){document.querySelector('#signIn')?.addEventListener('click',()=>attempt(()=>act('sign_in')));document.querySelector('#follow')?.addEventListener('click',()=>attempt(()=>state.following?Promise.resolve():act('follow')));document.querySelector('#message')?.addEventListener('click',()=>go('messages'));document.querySelectorAll('.join').forEach(b=>b.addEventListener('click',()=>attempt(async()=>{await act('select_tier',{tier:b.dataset.tier});checkoutOpen=true;render()})));document.querySelector('#cancel')?.addEventListener('click',()=>attempt(()=>act('cancel_membership'),'Membership cancelled'));document.querySelector('#send')?.addEventListener('click',()=>attempt(()=>act('send_message',{message:document.querySelector('#messageBody').value}),'Message sent'));document.querySelector('#close')?.addEventListener('click',()=>{checkoutOpen=false;render()});document.querySelector('#done')?.addEventListener('click',()=>{checkoutOpen=false;completed=false;go('public')});document.querySelector('#confirm')?.addEventListener('click',()=>attempt(async()=>{await act('checkout');completed=true;render()}));document.querySelectorAll('input[name="publishMode"]').forEach(r=>r.addEventListener('change',()=>{const scheduled=r.value==='schedule'&&r.checked;document.querySelector('#scheduleField').classList.toggle('hidden',!scheduled);document.querySelector('#publishPost').textContent=scheduled?'Schedule post':'Publish post'}));document.querySelector('#videoUrl')?.addEventListener('input',e=>{document.querySelector('#videoPreview').textContent=e.target.value?'Video: '+e.target.value:'No video link added'});document.querySelector('#publishPost')?.addEventListener('click',()=>attempt(async()=>{const mode=document.querySelector('input[name="publishMode"]:checked').value;await act('create_post',{title:document.querySelector('#postTitle').value,body:document.querySelector('#postBody').value,video_url:document.querySelector('#videoUrl').value,audience:document.querySelector('#postAudience').value,publish_mode:mode,scheduled_at:document.querySelector('#scheduledAt').value});go('posts')},'Post saved'));document.querySelector('#requestPayout')?.addEventListener('click',()=>attempt(()=>act('request_payout',{amount:Number(document.querySelector('#payoutAmount').value)}),'Payout requested'));document.querySelector('#saveMethod')?.addEventListener('click',()=>attempt(()=>act('set_payout_method',{type:document.querySelector('#methodType').value,last_four:document.querySelector('#methodLastFour').value}),'Payout method updated'));document.querySelectorAll('[data-member]').forEach(b=>b.addEventListener('click',()=>{selectedMember=b.dataset.member;render()}));document.querySelector('#sendMemberMessage')?.addEventListener('click',()=>attempt(()=>act('send_member_message',{member_id:selectedMember,message:document.querySelector('#memberMessage').value}),'Message sent'))}
function render(){document.querySelector('#viewerInitial').textContent=(state.viewer.name||'A')[0].toUpperCase();const views={public:publicView,messages:messagesView,dashboard:dashboardView,posts:postsView,'new-post':newPostView,payouts:payoutsView,members:membersView};const patronView=view==='public'||view==='messages';document.querySelector('#app').innerHTML=!state.viewer.signed_in&&patronView&&view!=='public'?authView():(views[view]||publicView)();document.querySelector('#modal').innerHTML=modal();bind()}
load().catch(e=>{document.querySelector('#app').innerHTML='<section class="auth"><h1>Unable to load</h1><p>'+esc(e.message)+'</p></section>'});
</script>
</body>
</html>`
