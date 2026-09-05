package textinput

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/apteva/apps/mcp/computer/internal/browser/cdputil"
	"strings"

	cdpinput "github.com/chromedp/cdproto/input"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"

	"github.com/apteva/apps/mcp/computer/internal/browser/domselector"
	"github.com/apteva/apps/mcp/computer/internal/browser/keyinput"
)

type Target struct {
	ID       string
	Selector string
	X        int
	Y        int
	HasPoint bool
}

type SetRequest struct {
	Text        string `json:"text"`
	Mode        string `json:"mode,omitempty"`
	NewlineMode string `json:"newline_mode,omitempty"`
}

type SetResult struct {
	Kind          string   `json:"kind"`
	Selector      string   `json:"selector,omitempty"`
	Label         string   `json:"label,omitempty"`
	InputType     string   `json:"input_type,omitempty"`
	PreviousValue string   `json:"previous_value"`
	Value         string   `json:"value"`
	Changed       bool     `json:"changed"`
	Mode          string   `json:"mode"`
	NewlineMode   string   `json:"newline_mode"`
	RenderedText  string   `json:"rendered_text"`
	Paragraphs    []string `json:"paragraphs"`
	Verified      bool     `json:"verified"`
	Verification  string   `json:"verification"`
}

type setPreparation struct {
	OK              bool   `json:"ok"`
	Error           string `json:"error,omitempty"`
	Kind            string `json:"kind"`
	Selector        string `json:"selector,omitempty"`
	Label           string `json:"label,omitempty"`
	InputType       string `json:"input_type,omitempty"`
	PreviousValue   string `json:"previous_value"`
	RequestedValue  string `json:"requested_value"`
	TextToInsert    string `json:"text_to_insert"`
	Mode            string `json:"mode"`
	NewlineMode     string `json:"newline_mode"`
	ContentEditable bool   `json:"contenteditable"`
	SingleLine      bool   `json:"single_line"`
}

// Set replaces or appends text in a native control or rich-text editor.
//
// Contenteditable editors must be updated through the browser editing path.
// Directly replacing DOM children appears to work briefly in controlled
// editors such as ProseMirror, but their model then restores the old DOM. It
// can also destroy contenteditable=false widgets embedded in the editor. We
// therefore select only the editable range and use CDP Input.insertText, then
// verify the value over a settling window after the editor has reconciled it.
func Set(ctx context.Context, target Target, req SetRequest) (SetResult, error) {
	mode := strings.ToLower(strings.TrimSpace(req.Mode))
	if mode == "" {
		mode = "replace"
	}
	if mode != "replace" && mode != "append" {
		return SetResult{}, errors.New("set_text: mode must be replace or append")
	}
	newlineMode := strings.ToLower(strings.TrimSpace(req.NewlineMode))
	if newlineMode == "" {
		newlineMode = "preserve"
	}
	if newlineMode != "preserve" && newlineMode != "compact" {
		return SetResult{}, errors.New("set_text: newline_mode must be preserve or compact")
	}
	req.Mode = mode
	req.NewlineMode = newlineMode

	targetJSON, _ := json.Marshal(target)
	reqJSON, _ := json.Marshal(req)
	prepareJS := fmt.Sprintf(`(function(target, req) {
  req = {
    Text: String(req.Text ?? req.text ?? ''),
    Mode: String(req.Mode ?? req.mode ?? 'replace').toLowerCase(),
    NewlineMode: String(req.NewlineMode ?? req.newline_mode ?? 'preserve').toLowerCase()
  };
  function norm(s) { return String(s || '').replace(/\s+/g, ' ').trim(); }
  function normalizeLines(value) { return String(value || '').replace(/\r\n/g, '\n').replace(/\r/g, '\n'); }
  function compactNewlines(value) { return normalizeLines(value).replace(/\n{2,}/g, '\n'); }
  function paragraphList(value) {
    var lines=normalizeLines(value).split('\n'),paragraphs=[],current=[];
    lines.forEach(function(line){
      if(line===''){paragraphs.push(current.join('\n').replace(/[ \t]+$/gm,'').trim());current=[];}
      else current.push(line);
    });
    paragraphs.push(current.join('\n').replace(/[ \t]+$/gm,'').trim());
    return paragraphs;
  }
  function editableText(root) {
    var protectedNodes=Array.from(root.querySelectorAll('[contenteditable="false"]'));
    var displays=protectedNodes.map(function(node){return {value:node.style.getPropertyValue('display'),priority:node.style.getPropertyPriority('display')};});
    try {
      protectedNodes.forEach(function(node){node.style.setProperty('display','none','important');});
      return normalizeLines(root.innerText||root.textContent||'').replace(/\u00a0/g,' ').replace(/^\n+|\n+$/g,'');
    } finally {
      protectedNodes.forEach(function(node,index){
        var old=displays[index];
        if(old.value)node.style.setProperty('display',old.value,old.priority);else node.style.removeProperty('display');
      });
    }
  }
%s
  function labelFor(el) {
    if (!el) return '';
    var aria = norm(el.getAttribute && el.getAttribute('aria-label'));
    if (aria) return aria;
    var labelled = norm(el.getAttribute && el.getAttribute('aria-labelledby'));
    if (labelled) {
      var doc=el.ownerDocument||document;
      var joined=norm(labelled.split(/\s+/).map(function(id){var node=doc.getElementById(id);return node?(node.innerText||node.textContent||''):'';}).join(' '));
      if (joined) return joined;
    }
    if (el.id) {
      var lab=document.querySelector('label[for="'+CSS.escape(el.id)+'"]');
      if (lab) return norm(lab.textContent);
    }
    var closestLabel=el.closest&&el.closest('label');
    if (closestLabel) return norm(closestLabel.textContent);
    return norm(el.getAttribute&&(el.getAttribute('title')||el.getAttribute('placeholder')));
  }
  function isTextTarget(el) {
    if (!el || !el.matches) return false;
    if (el.matches('textarea')) return true;
    if (el.matches('input:not([type]),input[type=""],input[type="text"],input[type="search"],input[type="url"],input[type="tel"],input[type="email"],input[type="password"],input[type="number"]')) return true;
    return el.matches('[contenteditable="true"],[contenteditable=""],[role="textbox"]');
  }
  function resolveTarget() {
    if (target.ID) {
      var state = window.__aptevaComputerSOM, saved = state && state.targets && state.targets[target.ID];
      // A stable identity must never fall through to an old coordinate.
      return saved && saved.element && saved.element.isConnected ? saved.element : null;
    }
    var el=null;
    if (target.Selector) el=document.querySelector(target.Selector);
    if (!el && target.HasPoint) el=document.elementFromPoint(target.X,target.Y);
    if (!el) return null;
    if (isTextTarget(el)) return el;
    return el.closest ? (el.closest('textarea,input:not([type="hidden"]),[contenteditable="true"],[contenteditable=""],[role="textbox"]')||el) : el;
  }
  function setNativeValue(el,value) {
    var proto=el instanceof HTMLTextAreaElement?HTMLTextAreaElement.prototype:HTMLInputElement.prototype;
    var desc=Object.getOwnPropertyDescriptor(proto,'value');
    if(desc&&desc.set)desc.set.call(el,value);else el.value=value;
  }
  function protectedChild(child) {
    return child.nodeType===Node.ELEMENT_NODE && (child.getAttribute('contenteditable')==='false' || !!child.querySelector('[contenteditable="false"]'));
  }
  var el=resolveTarget();
  if(!el)return {error:'set_text: target not found'};
  var isNative=el.matches&&el.matches('input,textarea');
  var isContentEditable=!isNative&&(!!el.isContentEditable||(el.getAttribute&&el.getAttribute('role')==='textbox'));
  if(!isNative&&!isContentEditable)return {error:'set_text: target is not a text input, textarea, contenteditable, or textbox'};
  if(isNative&&(el.disabled||el.readOnly))return {error:'set_text: target is disabled or readonly'};
  var previous=normalizeLines(isContentEditable?editableText(el):(el.value||''));
  var incoming=req.NewlineMode==='compact'?compactNewlines(req.Text):normalizeLines(req.Text);
  var requested=req.Mode==='append'?previous+incoming:incoming;
  try{el.focus();}catch(e){}
  if(isContentEditable){
    var children=Array.from(el.childNodes),start=0,end=children.length;
    while(start<end&&protectedChild(children[start]))start++;
    while(end>start&&protectedChild(children[end-1]))end--;
    for(var i=start;i<end;i++){
      if(protectedChild(children[i]))return {error:'set_text: cannot safely replace editable text around an embedded contenteditable=false widget'};
    }
    var range=document.createRange();
    if(req.Mode==='append')range.setStart(el,end);else range.setStart(el,start);
    range.setEnd(el,end);
    if(req.Mode==='append')range.collapse(true);
    var selection=(el.ownerDocument||document).getSelection();
    selection.removeAllRanges();selection.addRange(range);
  }else{
    setNativeValue(el,requested);
    try{el.dispatchEvent(new InputEvent('input',{bubbles:true,inputType:'insertText',data:incoming}));}catch(e){el.dispatchEvent(new Event('input',{bubbles:true}));}
    el.dispatchEvent(new Event('change',{bubbles:true}));
  }
  return {
    ok:true,
    kind:isContentEditable?'contenteditable':'native',
    selector:cssPath(el),
    label:labelFor(el),
    input_type:isContentEditable?'contenteditable':String(el.type||el.tagName||'').toLowerCase(),
    previous_value:previous,
    requested_value:requested,
    text_to_insert:req.Mode==='append'?incoming:requested,
    mode:req.Mode,
    newline_mode:req.NewlineMode,
    contenteditable:isContentEditable,
    single_line:isNative&&String(el.tagName||'').toLowerCase()==='input'
  };
})(%s,%s)`, domselector.UniqueCSSPathFunction, string(targetJSON), string(reqJSON))

	var prepared setPreparation
	if err := cdputil.Run(ctx, chromedp.Evaluate(prepareJS, &prepared)); err != nil {
		return SetResult{}, err
	}
	if prepared.Error != "" {
		return SetResult{}, errors.New(prepared.Error)
	}
	if !prepared.OK {
		return SetResult{}, errors.New("set_text failed to prepare target")
	}
	if prepared.ContentEditable {
		if err := insertRichText(ctx, prepared.TextToInsert); err != nil {
			return SetResult{}, fmt.Errorf("set_text native editor insertion: %w", err)
		}
	}

	selectorJSON, _ := json.Marshal(prepared.Selector)
	expectedJSON, _ := json.Marshal(prepared.RequestedValue)
	verifyJS := fmt.Sprintf(`(async function(selector,expected) {
  function normalizeLines(value){return String(value||'').replace(/\r\n/g,'\n').replace(/\r/g,'\n');}
  function paragraphList(value){
    var lines=normalizeLines(value).split('\n'),paragraphs=[],current=[];
    lines.forEach(function(line){if(line===''){paragraphs.push(current.join('\n').replace(/[ \t]+$/gm,'').trim());current=[];}else current.push(line);});
    paragraphs.push(current.join('\n').replace(/[ \t]+$/gm,'').trim());return paragraphs;
  }
  function editableText(root){
    var protectedNodes=Array.from(root.querySelectorAll('[contenteditable="false"]'));
    var displays=protectedNodes.map(function(node){return {value:node.style.getPropertyValue('display'),priority:node.style.getPropertyPriority('display')};});
    try{
      protectedNodes.forEach(function(node){node.style.setProperty('display','none','important');});
      return normalizeLines(root.innerText||root.textContent||'').replace(/\u00a0/g,' ').replace(/^\n+|\n+$/g,'');
    }finally{
      protectedNodes.forEach(function(node,index){var old=displays[index];if(old.value)node.style.setProperty('display',old.value,old.priority);else node.style.removeProperty('display');});
    }
  }
  function read(){
    var el=document.querySelector(selector);
    if(!el)return {error:'set_text: target disappeared during verification'};
    var contenteditable=!el.matches('input,textarea')&&(!!el.isContentEditable||el.getAttribute('role')==='textbox');
    var text=normalizeLines(contenteditable?editableText(el):(el.value||''));
    return {el:el,text:text,paragraphs:paragraphList(text)};
  }
  var stableCount=0,last=null,current=null;
  for(var i=0;i<7;i++){
    if(i)await new Promise(function(resolve){setTimeout(resolve,100);});
    current=read();if(current.error)return current;
    if(current.text===last)stableCount++;else stableCount=0;
    last=current.text;
  }
  try{current.el.blur();}catch(e){}
  await new Promise(function(resolve){setTimeout(resolve,100);});
  current=read();if(current.error)return current;
  var requested=paragraphList(expected);
  var verified=%t?current.text===normalizeLines(expected):(requested.length===current.paragraphs.length&&requested.every(function(p,i){return p===current.paragraphs[i];}));
  return {ok:true,text:current.text,paragraphs:current.paragraphs,verified:verified,stable:stableCount>=2};
})(%s,%s)`, prepared.SingleLine, string(selectorJSON), string(expectedJSON))
	var verified struct {
		OK         bool     `json:"ok"`
		Error      string   `json:"error,omitempty"`
		Text       string   `json:"text"`
		Paragraphs []string `json:"paragraphs"`
		Verified   bool     `json:"verified"`
		Stable     bool     `json:"stable"`
	}
	if err := cdputil.Run(ctx, chromedp.Evaluate(verifyJS, &verified, func(p *runtime.EvaluateParams) *runtime.EvaluateParams {
		return p.WithAwaitPromise(true)
	})); err != nil {
		return SetResult{}, err
	}
	if verified.Error != "" {
		return SetResult{}, errors.New(verified.Error)
	}

	result := SetResult{
		Kind:          prepared.Kind,
		Selector:      prepared.Selector,
		Label:         prepared.Label,
		InputType:     prepared.InputType,
		PreviousValue: prepared.PreviousValue,
		Value:         verified.Text,
		Changed:       prepared.PreviousValue != verified.Text,
		Mode:          prepared.Mode,
		NewlineMode:   prepared.NewlineMode,
		RenderedText:  verified.Text,
		Paragraphs:    verified.Paragraphs,
		Verified:      verified.Verified && verified.Stable,
		Verification:  "paragraphs_stable",
	}
	if prepared.SingleLine {
		result.Verification = "scalar_stable"
	}
	if !verified.Verified {
		if prepared.SingleLine {
			return result, fmt.Errorf("set_text value mismatch after editor reconciliation: requested %q, actual single-line value %q", prepared.RequestedValue, verified.Text)
		}
		return result, fmt.Errorf("set_text rendered text mismatch after editor reconciliation: requested %q, actual single-line value %q", prepared.RequestedValue, verified.Text)
	}
	if !verified.Stable {
		return result, errors.New("set_text value did not stabilize after editor reconciliation")
	}
	return result, nil
}

func insertRichText(ctx context.Context, value string) error {
	value = strings.ReplaceAll(strings.ReplaceAll(value, "\r\n", "\n"), "\r", "\n")
	if value == "" {
		return keyinput.Dispatch(ctx, "Backspace", "[set_text]")
	}
	paragraphs := strings.Split(value, "\n\n")
	for paragraphIndex, paragraph := range paragraphs {
		if paragraphIndex > 0 {
			if err := dispatchRichEnter(ctx, false); err != nil {
				return err
			}
		}
		lines := strings.Split(paragraph, "\n")
		for lineIndex, line := range lines {
			if lineIndex > 0 {
				if err := dispatchRichEnter(ctx, true); err != nil {
					return err
				}
			}
			if line == "" {
				continue
			}
			if err := cdputil.Run(ctx, cdpinput.InsertText(line)); err != nil {
				return err
			}
		}
	}
	return nil
}

func dispatchRichEnter(ctx context.Context, shift bool) error {
	mods := cdpinput.ModifierNone
	if shift {
		mods = cdpinput.ModifierShift
	}
	down := cdpinput.DispatchKeyEvent(cdpinput.KeyDown).
		WithKey("Enter").WithCode("Enter").
		WithText("\r").WithUnmodifiedText("\r").
		WithWindowsVirtualKeyCode(13).WithModifiers(mods)
	up := cdpinput.DispatchKeyEvent(cdpinput.KeyUp).
		WithKey("Enter").WithCode("Enter").
		WithWindowsVirtualKeyCode(13).WithModifiers(mods)
	return cdputil.Run(ctx, down, up)
}
