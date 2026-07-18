package main

import (
	"errors"
	"strings"
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
		"posts": []any{
			map[string]any{"id": "post-1", "title": "Location notes: the old observatory", "date": "July 16", "excerpt": "A first look at the place where our next short film begins.", "locked": false},
			map[string]any{"id": "post-2", "title": "Members: rough cut and director commentary", "date": "July 11", "excerpt": "Watch the new twelve-minute cut and leave your notes before picture lock.", "locked": membership == nil},
			map[string]any{"id": "post-3", "title": "June production journal", "date": "June 28", "excerpt": "Casting updates, practical effects tests, and what comes next.", "locked": membership == nil},
		},
	}
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
	default:
		return "", nil, errors.New("unsupported action")
	}
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
</head>
<body>
<div class="notice">Test environment · No real payment or message will be sent</div>
<header class="top"><button class="brand" data-nav="creator">PATREON</button><input class="search" aria-label="Search creators" placeholder="Find a creator"><nav class="nav"><button class="link" data-nav="creator">Explore</button><button class="link" data-nav="messages">Messages</button><div class="avatar-sm" id="viewerInitial">A</div></nav></header>
<main id="app"></main>
<div id="modal"></div>
<script>
const match=location.pathname.match(/^(.*\/fixtures\/[^/]+\/[^/]+\/[^/]+\/)/);const base=match?match[1]:location.pathname;let state=null;let view=location.pathname.includes('/messages')?'messages':'creator';let checkoutOpen=false;let completed=false;
const esc=v=>String(v??'').replace(/[&<>"']/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
async function load(){const r=await fetch(base+'api/state');const body=await r.json();state=body.state;render()}
async function act(action,input={}){const r=await fetch(base+'api/action',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({action,input})});const body=await r.json();if(!r.ok)throw new Error(body.error||'Action failed');state=body.state;render();return body}
function go(next){view=next;history.pushState({},'',base+(next==='messages'?'messages':'creator/'+state.creator.slug));render()}
document.addEventListener('click',e=>{const nav=e.target.closest('[data-nav]');if(nav)go(nav.dataset.nav)});window.addEventListener('popstate',()=>{view=location.pathname.includes('/messages')?'messages':'creator';render()});
function creatorView(){const c=state.creator,m=state.membership;const initials=esc(c.name.split(/\s+/).map(x=>x[0]).slice(0,2).join(''));const posts=state.posts.map(p=>'<article class="post"><time>'+esc(p.date)+'</time><h3>'+esc(p.title)+'</h3><p>'+esc(p.excerpt)+'</p>'+(p.locked?'<div class="locked">Members-only post</div>':'')+'</article>').join('');const memberships=m&&m.status==='active'?'<div class="member-box"><h3>'+esc(m.tier_name)+' member</h3><p>Your membership is active at $'+Number(m.price).toFixed(0)+' per month.</p><button class="secondary" id="cancel">Cancel membership</button></div>':c.tiers.map(t=>'<article class="tier"><h3>'+esc(t.name)+'</h3><div class="price">$'+Number(t.price).toFixed(0)+' <small>/ month</small></div><p>'+esc(t.description)+'</p><button class="primary join" data-tier="'+esc(t.id)+'">Join</button></article>').join('');return '<section class="cover"></section><section class="creator"><div class="identity"><div class="avatar">'+initials+'</div><div class="identity-copy"><h1>'+esc(c.name)+'</h1><p>'+esc(c.tagline)+'</p></div><div class="actions"><button class="secondary" id="follow">'+(state.following?'Following':'Follow')+'</button><button class="primary" id="message">Message</button></div></div><div class="stats"><span><b>'+Number(c.members).toLocaleString()+'</b> members</span><span><b>'+Number(c.posts_count).toLocaleString()+'</b> posts</span></div><div class="layout"><section><h2 class="section-title">Latest posts</h2>'+posts+'</section><aside><h2 class="section-title">Choose your membership</h2>'+memberships+'</aside></div></section>'}
function messagesView(){const sent=state.messages.map(m=>'<div class="sent"><b>Sent to '+esc(m.recipient)+'</b><p>'+esc(m.body)+'</p></div>').join('');return '<section class="messages"><h1>Message '+esc(state.creator.name)+'</h1><div class="composer"><textarea id="messageBody" aria-label="Message" placeholder="Write a message"></textarea><div class="row"><button class="primary" id="send">Send message</button></div></div>'+sent+'</section>'}
function authView(){return '<section class="auth"><h1>Welcome back</h1><p>Sign in to follow creators, join memberships, and send messages.</p><button class="primary" id="signIn">Continue as '+esc(state.viewer.name)+'</button></section>'}
function modal(){if(!checkoutOpen)return'';const tier=state.creator.tiers.find(t=>t.id===state.checkout.selected_tier);if(completed)return '<div class="modal-wrap"><div class="modal success"><h2>Welcome to '+esc(tier.name)+'</h2><p>Your membership is active. Member posts are now available.</p><button class="primary" id="done">Done</button></div></div>';const error=state.checkout.error?'<p class="error">'+esc(state.checkout.error)+'</p>':'';return '<div class="modal-wrap"><div class="modal"><div class="modal-head"><h2>Complete your membership</h2><button class="close" id="close" aria-label="Close">×</button></div><div class="modal-body"><div class="order"><span>'+esc(tier.name)+'</span><b>$'+Number(tier.price).toFixed(0)+' / month</b></div><div class="fields"><label><span>Card number</span><input value="4242 4242 4242 4242" aria-label="Card number"></label><label><span>Expiry</span><input value="12/30" aria-label="Expiry"></label><label><span>CVC</span><input value="123" aria-label="CVC"></label></div>'+error+'<button class="primary" id="confirm" style="width:100%;margin-top:18px">Confirm membership</button></div></div></div>'}
function bind(){document.querySelector('#signIn')?.addEventListener('click',async()=>{await act('sign_in')});document.querySelector('#follow')?.addEventListener('click',async()=>{if(!state.following)await act('follow')});document.querySelector('#message')?.addEventListener('click',()=>go('messages'));document.querySelectorAll('.join').forEach(b=>b.addEventListener('click',async()=>{try{await act('select_tier',{tier:b.dataset.tier});checkoutOpen=true;render()}catch(e){alert(e.message)}}));document.querySelector('#cancel')?.addEventListener('click',async()=>{if(confirm('Cancel this membership?'))await act('cancel_membership')});document.querySelector('#send')?.addEventListener('click',async()=>{const message=document.querySelector('#messageBody').value;try{await act('send_message',{message})}catch(e){alert(e.message)}});document.querySelector('#close')?.addEventListener('click',()=>{checkoutOpen=false;render()});document.querySelector('#done')?.addEventListener('click',()=>{checkoutOpen=false;completed=false;go('creator')});document.querySelector('#confirm')?.addEventListener('click',async()=>{try{await act('checkout');completed=true;render()}catch(e){alert(e.message)}})}
function render(){document.querySelector('#viewerInitial').textContent=(state.viewer.name||'A')[0].toUpperCase();document.querySelector('#app').innerHTML=!state.viewer.signed_in&&view!=='creator'?authView():(view==='messages'?messagesView():creatorView());document.querySelector('#modal').innerHTML=modal();bind()}
load().catch(e=>{document.querySelector('#app').innerHTML='<section class="auth"><h1>Unable to load</h1><p>'+esc(e.message)+'</p></section>'});
</script>
</body>
</html>`
