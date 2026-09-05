package stability

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/apteva/apps/mcp/computer/internal/browser/cdputil"

	computer "github.com/apteva/apps/mcp/computer/internal/browser/api"
	cdpruntime "github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

// ObserveMedia reads only browser-visible evidence. In particular, a loaded
// player is not a claim that the player contains the asset the caller wanted.
func ObserveMedia(ctx context.Context) (computer.MediaObservation, error) {
	var observation computer.MediaObservation
	err := cdputil.Run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		value, exception, err := cdpruntime.Evaluate("(" + mediaObservationFunction + ")()").WithReturnByValue(true).Do(ctx)
		if err != nil {
			return err
		}
		if exception != nil {
			return fmt.Errorf("observe media: %s", exception.Text)
		}
		if value == nil || len(value.Value) == 0 {
			return fmt.Errorf("observe media returned no DOM state")
		}
		return json.Unmarshal(value.Value, &observation)
	}))
	return observation, err
}

// mediaObservationFunction is shared by one-shot observations and wait_for so
// every CDP backend applies exactly the same terminal-state semantics.
const mediaObservationFunction = `function(){
  function clean(value){return String(value||'').replace(/\s+/g,' ').trim();}
  function visible(el){if(!el)return false;var r=el.getBoundingClientRect();if(r.width<2||r.height<2)return false;try{if(el.checkVisibility&&!el.checkVisibility({checkOpacity:true,checkVisibilityCSS:true}))return false;}catch(e){}for(var n=el;n&&n.nodeType===1;n=n.parentElement){var s=getComputedStyle(n);if(s.display==='none'||s.visibility==='hidden'||parseFloat(s.opacity||'1')<0.1)return false;}return true;}
  function firstVisible(selector,root){var nodes=(root||document).querySelectorAll(selector);for(var i=0;i<nodes.length;i++)if(visible(nodes[i]))return nodes[i];return null;}
  function textOf(el){return clean(el&&(el.getAttribute('aria-label')||el.getAttribute('title')||el.innerText||el.textContent));}
  function hostIdentity(value){try{var host=new URL(value,location.href).hostname.toLowerCase().replace(/^www\./,'');var labels=host.split('.');while(labels.length>2&&/^(iframe|embed|player|video|audio|media|cdn|www)$/.test(labels[0]))labels.shift();return labels.join('.');}catch(e){return '';}}
  function providerFor(src){var value=String(src||'');if(!value)return '';try{var parsed=new URL(value,location.href),declared=clean(parsed.searchParams.get('display_name')||parsed.searchParams.get('provider')||'').toLowerCase();if(declared)return declared;var nested=parsed.searchParams.get('src')||parsed.searchParams.get('url')||'';return hostIdentity(nested||value)||'unknown';}catch(e){return hostIdentity(value)||'unknown';}}
  function mediaLikeSource(src){var value=String(src||'').toLowerCase();if(/(video|audio|media|player|embed)/.test(value))return true;try{var parsed=new URL(value,location.href);return !!(parsed.searchParams.get('display_name')||parsed.searchParams.get('provider')||parsed.searchParams.get('media_width')||parsed.searchParams.get('media_height')||parsed.searchParams.get('image'));}catch(e){return false;}}
  function mediaFrame(){var frames=document.querySelectorAll('iframe[src]'),fallback=null;for(var i=0;i<frames.length;i++){var frame=frames[i];if(!visible(frame))continue;var src=frame.src||frame.getAttribute('src')||'',hint=clean((frame.title||'')+' '+(frame.getAttribute('aria-label')||'')).toLowerCase();if(mediaLikeSource(src)||/(video|audio|media|player|embed)/.test(hint))return frame;if(!fallback&&hint)fallback=frame;}return fallback&&/(video|audio|media|player|embed)/.test(clean(fallback.title||''))?fallback:null;}
  var video=firstVisible('video'),audio=firstVisible('audio'),frame=mediaFrame();
  var mediaNode=video||audio||frame||firstVisible('[data-testid*="media" i],[data-testid*="video" i],[data-testid*="audio" i],[class*="media-embed" i],[class*="video-embed" i],[class*="audio-embed" i],[aria-label*="video player" i],[aria-label*="audio player" i]');
  var container=mediaNode&&(mediaNode.closest('[data-testid*="media" i],[data-testid*="video" i],[data-testid*="audio" i],[class*="media" i],[class*="video" i],[class*="audio" i],[class*="embed" i],figure')||mediaNode.parentElement||mediaNode);
  var src=frame?(frame.src||frame.getAttribute('src')||''):'';
  var provider=providerFor(src||(video&&(video.currentSrc||video.src))||(audio&&(audio.currentSrc||audio.src))||'');
  var thumb=null,thumbnailURL='';
  if(video&&video.poster){thumb=video;thumbnailURL=video.poster;}
  if(!thumb&&container){thumb=firstVisible('img[src],picture img[src]',container);if(thumb)thumbnailURL=thumb.currentSrc||thumb.src||'';}
  if(!thumbnailURL&&src){try{var configuredImage=new URL(src,location.href).searchParams.get('image');if(configuredImage)thumbnailURL=configuredImage;}catch(e){}}
  var durationSeconds=0,durationText='';
  var timed=video||audio;if(timed&&isFinite(Number(timed.duration))&&Number(timed.duration)>0)durationSeconds=Math.round(Number(timed.duration)*1000)/1000;
  if(container){var durationMatch=clean(container.innerText||container.textContent).match(/(?:^|\s)(\d{1,2}:\d{2}(?::\d{2})?)(?:\s|$)/);if(durationMatch)durationText=durationMatch[1];if(!durationText){var durationNodes=container.querySelectorAll('[aria-label*="duration" i],time,span');for(var di=0;di<durationNodes.length;di++){var durationCandidate=textOf(durationNodes[di]).match(/\b(\d{1,2}:\d{2}(?::\d{2})?)\b/);if(durationCandidate){durationText=durationCandidate[1];break;}}}}
  var config=false;if(container){var controls=container.querySelectorAll('button,[role="button"],[role="menu"],[role="menuitem"]');for(var ci=0;ci<controls.length;ci++){if(!visible(controls[ci]))continue;var controlName=textOf(controls[ci]).toLowerCase();if(/\b(edit|remove|replace|configure|settings|options|menu)\b/.test(controlName)){config=true;break;}}}
  var errorText='',known="This URL doesn't look like a video or audio file";
  var bodyText=clean((document.body&&document.body.innerText)||(document.documentElement&&document.documentElement.innerText)||'');
  if(bodyText.toLowerCase().indexOf(known.toLowerCase())>=0)errorText=known;
  if(!errorText){var errors=document.querySelectorAll('[role="alert"],[aria-live="assertive"],[data-state="error"],[class*="error" i]');for(var ei=0;ei<errors.length;ei++){if(!visible(errors[ei]))continue;var errorCandidate=textOf(errors[ei]);if(/(doesn.t look like (?:a )?video|unable to (?:load|embed)|invalid (?:video|audio|media)|(?:video|audio|media).*(?:unsupported|not supported)|couldn.t (?:load|embed)|failed to (?:load|embed))/i.test(errorCandidate)){errorText=errorCandidate;break;}}}
  var loading=false,loadingRoot=container||document;var loadingNodes=loadingRoot.querySelectorAll('[aria-busy="true"],[data-loading="true"],[data-state="loading"],[role="progressbar"],[aria-label*="loading" i],[aria-label*="processing" i],[class*="spinner" i]');for(var li=0;li<loadingNodes.length;li++)if(visible(loadingNodes[li])){loading=true;break;}
  if(!loading&&container)loading=/(loading|processing|embedding)\s+(video|audio|media)/i.test(clean(container.innerText||container.textContent));
  var playerVisible=!!(video||audio||frame),status='unknown';
  if(errorText)status='rejected';else if(playerVisible||thumb||durationSeconds>0||durationText||config)status='loaded';else if(loading||mediaNode)status='loading';
  function saveStateFor(value){var text=clean(value);if(/(couldn.t save|failed to save|error saving|draft not saved)/i.test(text))return 'error';if(/^(saving|saving draft|autosaving)(\b|\.{3}|…)/i.test(text))return 'saving';if(/^(saved|draft saved|changes saved|saved just now)[.!]?$/i.test(text))return 'saved';return '';}
  var draftState='unknown',draftText='',saveNodes=document.querySelectorAll('[role="status"],[aria-live],[data-save-state],[data-state]');for(var si=0;si<saveNodes.length;si++){if(!visible(saveNodes[si]))continue;var saveText=textOf(saveNodes[si]),saveState=saveStateFor(saveText);if(saveState){draftState=saveState;draftText=saveText;break;}}
  if(draftState==='unknown'&&document.body){var walker=document.createTreeWalker(document.body,NodeFilter.SHOW_TEXT),scanned=0,node;while((node=walker.nextNode())&&scanned++<6000){var directText=clean(node.nodeValue),directState=saveStateFor(directText);if(!directState)continue;var parent=node.parentElement;if(!visible(parent))continue;var saveRect=parent.getBoundingClientRect(),topBar=saveRect.top>=0&&saveRect.top<180,semantic=!!parent.closest('header,nav,[role="banner"],[role="status"],[aria-live]');if(saveRect.width<=260&&saveRect.height<=90&&(topBar||semantic)){draftState=directState;draftText=directText;break;}}}
  return {media_embed_status:status,media_player_visible:playerVisible,media_iframe_visible:!!frame,media_thumbnail_visible:!!thumb,media_thumbnail_url:thumbnailURL,media_duration_text:durationText,media_duration_seconds:durationSeconds,media_configuration_present:config,media_error_text:errorText,media_iframe_src:src,media_provider:provider,draft_save_state:draftState,draft_save_text:draftText};
}`
