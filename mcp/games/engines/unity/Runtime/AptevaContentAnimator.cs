using UnityEngine;
namespace Apteva.Games {
[RequireComponent(typeof(SpriteRenderer))]
public sealed class AptevaContentAnimator : MonoBehaviour {
    public AptevaContentClip clip;
    private AptevaContentClip active; private SpriteRenderer renderer2D; private int frame; private float elapsed;
    void Awake() { renderer2D=GetComponent<SpriteRenderer>(); }
    void Update() {
        if(clip==null || clip.frames==null || clip.frames.Length==0 || clip.durationMs.Length!=clip.frames.Length) return;
        if(active!=clip){active=clip;frame=0;elapsed=0;}
        elapsed+=Time.deltaTime*1000;
        // Bound catch-up work after a suspended app resumes.
        int steps=0;
        while(elapsed>=Mathf.Max(1,clip.durationMs[frame]) && steps++<1000){
            elapsed-=Mathf.Max(1,clip.durationMs[frame]);
            if(frame+1<clip.frames.Length)frame++;else if(clip.loop)frame=0;else{elapsed=0;break;}
        }
        if(steps>=1000)elapsed=0;
        renderer2D.sprite=clip.frames[frame];
    }
}
