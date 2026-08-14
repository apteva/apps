// Package scrolltarget discovers and scrolls semantic DOM regions while
// reporting which container actually moved.
package scrolltarget

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/chromedp/cdproto/input"
	"github.com/chromedp/chromedp"

	computer "github.com/apteva/apps/mcp/computer/internal/browser/api"
)

const EnumerateScript = `(function(){
  var vw=window.innerWidth,vh=window.innerHeight;
  function clean(v){return String(v||'').replace(/\s+/g,' ').trim();}
  function labelledBy(el){var ids=clean(el.getAttribute&&el.getAttribute('aria-labelledby'));if(!ids)return '';return clean(ids.split(/\s+/).map(function(id){var n=document.getElementById(id);return n?(n.innerText||n.textContent||''):'';}).join(' '));}
  function nameFor(el,isDoc){
    if(isDoc)return 'Document';
    var name=clean((el.getAttribute&&el.getAttribute('aria-label'))||labelledBy(el)||(el.getAttribute&&el.getAttribute('title'))||(el.getAttribute&&el.getAttribute('data-testid'))||'');
    if(name)return name;
    var heading=el.querySelector&&el.querySelector(':scope > h1,:scope > h2,:scope > h3,:scope > header h1,:scope > header h2,:scope > header h3');
    if(heading){name=clean(heading.innerText||heading.textContent);if(name)return name.slice(0,80);}
    var role=clean(el.getAttribute&&el.getAttribute('role'));
    if(role)return role;
    var tag=(el.tagName||'').toLowerCase();
    if(tag==='aside')return 'Sidebar';if(tag==='main')return 'Main content';if(tag==='nav')return 'Navigation';
    return 'Scrollable region';
  }
  function roleFor(el,isDoc){if(isDoc)return 'document';return clean(el.getAttribute&&el.getAttribute('role'))||(el.tagName||'').toLowerCase()||'region';}
  function keyFor(el,isDoc){
    if(isDoc)return 'document';
    var attrs=['id','data-testid','data-test','data-qa','name'];
    for(var ai=0;ai<attrs.length;ai++){var av=clean(el.getAttribute&&el.getAttribute(attrs[ai]));if(av)return attrs[ai]+':'+av;}
    var parts=[];for(var n=el;n&&n.nodeType===1&&parts.length<12;n=n.parentElement){var tag=(n.tagName||'').toLowerCase(),index=1;for(var p=n.previousElementSibling;p;p=p.previousElementSibling)if(p.tagName===n.tagName)index++;parts.push(tag+':nth-of-type('+index+')');}
    return 'path:'+parts.reverse().join('>');
  }
  function idFor(el,isDoc){var key=String(location.pathname||'')+'|'+keyFor(el,isDoc),hash=2166136261;for(var i=0;i<key.length;i++){hash^=key.charCodeAt(i);hash=Math.imul(hash,16777619);}return isDoc?'scroll_document':'scroll_'+(hash>>>0).toString(36);}
  function visibleRect(el,isDoc){
    if(isDoc)return {left:0,top:0,right:vw,bottom:vh,width:vw,height:vh};
    var r=el.getBoundingClientRect(),s=getComputedStyle(el);if(r.width<8||r.height<8||r.right<=0||r.bottom<=0||r.left>=vw||r.top>=vh||s.display==='none'||s.visibility==='hidden'||parseFloat(s.opacity||'1')<0.1)return null;
    return {left:Math.max(0,r.left),top:Math.max(0,r.top),right:Math.min(vw,r.right),bottom:Math.min(vh,r.bottom),width:Math.min(vw,r.right)-Math.max(0,r.left),height:Math.min(vh,r.bottom)-Math.max(0,r.top)};
  }
  function qualifies(el,isDoc){
    var sx=isDoc?document.scrollingElement:el;if(!sx)return null;
    var canX=sx.scrollWidth>sx.clientWidth+2,canY=sx.scrollHeight>sx.clientHeight+2;
    if(!isDoc){var s=getComputedStyle(el),ox=s.overflowX,oy=s.overflowY;canX=canX&&/(auto|scroll|overlay)/.test(ox);canY=canY&&/(auto|scroll|overlay)/.test(oy);}
    if(!canX&&!canY)return null;var r=visibleRect(el,isDoc);if(!r)return null;
    return {node:sx,rect:r,canX:canX,canY:canY};
  }
  var records=[],nodes=[];
  var root=document.scrollingElement||document.documentElement,rootQ=qualifies(root,true);
  if(!rootQ){rootQ={node:root,rect:visibleRect(root,true),canX:false,canY:false};}
  nodes.push({el:root,isDoc:true,q:rootQ});
  var all=document.querySelectorAll('body *');for(var i=0;i<all.length;i++){var q=qualifies(all[i],false);if(q)nodes.push({el:all[i],isDoc:false,q:q});}
  function nearestParent(item){for(var p=item.el.parentElement;p;p=p.parentElement){for(var j=0;j<nodes.length;j++)if(nodes[j].el===p)return idFor(p,nodes[j].isDoc);}return rootQ&&!item.isDoc?'scroll_document':'';}
  for(var n=0;n<nodes.length;n++){var item=nodes[n],q=item.q,el=q.node,r=q.rect;records.push({id:idFor(item.el,item.isDoc),name:nameFor(item.el,item.isDoc),role:roleFor(item.el,item.isDoc),x:Math.round(r.left),y:Math.round(r.top),w:Math.round(r.width),h:Math.round(r.height),scroll_left:Math.round(el.scrollLeft||0),scroll_top:Math.round(el.scrollTop||0),max_scroll_x:Math.max(0,Math.round(el.scrollWidth-el.clientWidth)),max_scroll_y:Math.max(0,Math.round(el.scrollHeight-el.clientHeight)),can_scroll_x:q.canX,can_scroll_y:q.canY,parent_id:nearestParent(item),document:item.isDoc});}
  records.sort(function(a,b){if(a.document!==b.document)return a.document?1:-1;var aa=a.w*a.h,ba=b.w*b.h;return aa-ba||a.id.localeCompare(b.id);});
  return records;
})()`

func Enumerate(ctx context.Context) ([]computer.ScrollRegion, error) {
	var raw json.RawMessage
	if err := chromedp.Run(ctx, chromedp.Evaluate(EnumerateScript, &raw)); err != nil {
		return nil, err
	}
	var regions []computer.ScrollRegion
	if err := json.Unmarshal(raw, &regions); err != nil {
		return nil, err
	}
	return regions, nil
}

func Run(ctx context.Context, action computer.Action, display computer.DisplaySize) (computer.ScrollResult, error) {
	before, err := Enumerate(ctx)
	if err != nil {
		return computer.ScrollResult{}, fmt.Errorf("enumerate scroll regions: %w", err)
	}
	dx, dy, err := computer.ScrollDelta(action.Direction, action.Amount)
	if err != nil {
		return computer.ScrollResult{}, err
	}
	requested, pointX, pointY, err := resolve(before, action, display)
	if err != nil {
		return computer.ScrollResult{}, err
	}
	if action.ExpectedName != "" && !strings.EqualFold(strings.TrimSpace(action.ExpectedName), strings.TrimSpace(requested.Name)) {
		return computer.ScrollResult{}, fmt.Errorf("scroll rejected: expected target name %q but resolved %q", action.ExpectedName, requested.Name)
	}
	if action.ExpectedRole != "" && !strings.EqualFold(strings.TrimSpace(action.ExpectedRole), strings.TrimSpace(requested.Role)) {
		return computer.ScrollResult{}, fmt.Errorf("scroll rejected: expected target role %q but resolved %q", action.ExpectedRole, requested.Role)
	}
	if err := chromedp.Run(ctx, input.DispatchMouseEvent(input.MouseWheel, pointX, pointY).WithDeltaX(dx).WithDeltaY(dy)); err != nil {
		return computer.ScrollResult{}, err
	}
	time.Sleep(150 * time.Millisecond)
	after, err := Enumerate(ctx)
	if err != nil {
		return computer.ScrollResult{}, fmt.Errorf("enumerate post-scroll regions: %w", err)
	}
	result := movement(before, after, requested.ID)
	result.RequestedTargetID = requested.ID
	result.TargetName = requested.Name
	result.TargetRole = requested.Role
	result.RequestedTargetName = requested.Name
	result.RequestedTargetRole = requested.Role
	for _, region := range after {
		if region.ID == result.ActualTargetID {
			result.ActualTargetName = region.Name
			result.ActualTargetRole = region.Role
			break
		}
	}
	if result.ActualTargetID == "" {
		result.ActualTargetName = requested.Name
		result.ActualTargetRole = requested.Role
	}
	result.Regions = after
	result.Ambiguous = action.TargetID == "" && action.X == 0 && action.Y == 0 && len(verticalRegions(before)) > 1
	if result.ActualTargetID != "" && result.ActualTargetID != requested.ID {
		result.WrongTarget = true
	}
	return result, nil
}

func resolve(regions []computer.ScrollRegion, action computer.Action, display computer.DisplaySize) (computer.ScrollRegion, float64, float64, error) {
	if action.TargetID != "" {
		for _, region := range regions {
			if region.ID == action.TargetID {
				return region, float64(region.X + region.W/2), float64(region.Y + region.H/2), nil
			}
		}
		return computer.ScrollRegion{}, 0, 0, fmt.Errorf("scroll target %q is not present in the current document", action.TargetID)
	}
	x, y := action.X, action.Y
	if x == 0 && y == 0 {
		x, y = display.Width/2, display.Height/2
	}
	matches := make([]computer.ScrollRegion, 0)
	for _, region := range regions {
		if x >= region.X && y >= region.Y && x < region.X+region.W && y < region.Y+region.H {
			matches = append(matches, region)
		}
	}
	if len(matches) == 0 {
		return computer.ScrollRegion{}, 0, 0, fmt.Errorf("no scrollable region at %d,%d", x, y)
	}
	sort.SliceStable(matches, func(i, j int) bool { return matches[i].W*matches[i].H < matches[j].W*matches[j].H })
	return matches[0], float64(x), float64(y), nil
}

func movement(before, after []computer.ScrollRegion, requestedID string) computer.ScrollResult {
	beforeByID := make(map[string]computer.ScrollRegion, len(before))
	for _, r := range before {
		beforeByID[r.ID] = r
	}
	var result computer.ScrollResult
	bestMagnitude := 0
	for _, current := range after {
		old, ok := beforeByID[current.ID]
		if !ok {
			continue
		}
		dx, dy := current.ScrollLeft-old.ScrollLeft, current.ScrollTop-old.ScrollTop
		magnitude := abs(dx) + abs(dy)
		if magnitude > bestMagnitude {
			bestMagnitude = magnitude
			result.ActualTargetID = current.ID
			result.BeforeLeft = old.ScrollLeft
			result.BeforeTop = old.ScrollTop
			result.AfterLeft = current.ScrollLeft
			result.AfterTop = current.ScrollTop
			result.DeltaX = dx
			result.DeltaY = dy
		}
	}
	result.Moved = bestMagnitude > 0
	if !result.Moved {
		for _, current := range after {
			if current.ID == requestedID {
				old := beforeByID[current.ID]
				result.ActualTargetID = requestedID
				result.BeforeLeft = old.ScrollLeft
				result.BeforeTop = old.ScrollTop
				result.AfterLeft = current.ScrollLeft
				result.AfterTop = current.ScrollTop
				break
			}
		}
	}
	for _, current := range after {
		id := result.ActualTargetID
		if id == "" {
			id = requestedID
		}
		if current.ID != id {
			continue
		}
		result.AtStart = current.ScrollTop <= 0 && current.ScrollLeft <= 0
		result.AtEnd = (!current.CanScrollY || current.ScrollTop >= current.MaxScrollY) && (!current.CanScrollX || current.ScrollLeft >= current.MaxScrollX)
		break
	}
	return result
}

func verticalRegions(regions []computer.ScrollRegion) []computer.ScrollRegion {
	out := make([]computer.ScrollRegion, 0)
	for _, r := range regions {
		if r.CanScrollY {
			out = append(out, r)
		}
	}
	return out
}
func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func CloneRegions(in []computer.ScrollRegion) []computer.ScrollRegion {
	return append([]computer.ScrollRegion(nil), in...)
}
func CloneResult(in *computer.ScrollResult) *computer.ScrollResult {
	if in == nil {
		return nil
	}
	out := *in
	out.Regions = CloneRegions(in.Regions)
	return &out
}
