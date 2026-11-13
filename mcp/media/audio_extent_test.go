package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestAudioRedactionPreservesPresentationExtent(t *testing.T) {
	skipIfNoFFmpeg(t)
	for _, kind := range []string{"aac_padding_96k", "delayed_audio", "video_longer"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			source := filepath.Join(dir, "source.mov")
			videoDuration := "3"
			if kind == "video_longer" {
				videoDuration = "5"
			}
			args := []string{"-y", "-v", "error", "-f", "lavfi", "-i", "testsrc2=s=96x64:r=10:d=" + videoDuration}
			if kind == "delayed_audio" {
				args = append(args, "-itsoffset", "0.25")
			}
			args = append(args, "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=96000:duration=3", "-c:v", "libx264", "-c:a", "aac", "-movie_timescale", "1000", source)
			if log, err := exec.Command("ffmpeg", args...).CombinedOutput(); err != nil {
				t.Fatalf("fixture: %v %s", err, log)
			}
			if kind == "aac_padding_96k" {
				// Reproduce an authored presentation tail longer than the decoded
				// AAC payload, as in production source 144006. Header-only edits
				// cannot add the samples that the old redactor omitted.
				runtime := filepath.Join(dir, "runtime.py")
				_ = os.WriteFile(runtime, []byte(renderRuntimePython), 0600)
				code := `import importlib.util,sys,struct
from fractions import Fraction
s=importlib.util.spec_from_file_location('runtime',sys.argv[1]);m=importlib.util.module_from_spec(s);s.loader.exec_module(m)
p=sys.argv[2];m.patch_presentation_end(p,Fraction(307,100),{'vide':Fraction(3),'soun':Fraction(307,100)})
with open(p,'r+b') as f:
 off,head,data=m.movie_metadata(f)
 for a,b,k in m.movie_boxes(data,0,len(data)):
  if k!=b'trak':continue
  for x,y,t in m.movie_boxes(data,a,b):
   if t!=b'mdia':continue
   children=list(m.movie_boxes(data,x,y))
   if not any(t==b'hdlr' and data[x+8:x+12]==b'soun' for x,y,t in children):continue
   for x,y,t in children:
    if t==b'mdhd':
     v=data[x];scaleoff=x+(12 if v==0 else 20);scale=struct.unpack_from('>I',data,scaleoff)[0]
     struct.pack_into('>I' if v==0 else '>Q',data,scaleoff+4,round(3.07*scale))
    if t==b'minf':
     for sa,sb,sk in m.movie_boxes(data,x,y):
      if sk!=b'stbl':continue
      for ta,tb,tk in m.movie_boxes(data,sa,sb):
       if tk!=b'stts':continue
       n=struct.unpack_from('>I',data,ta+4)[0];entries=[struct.unpack_from('>II',data,ta+8+i*8) for i in range(n)]
       count,delta=entries[-1];assert count==1
       extra=294720-sum(c*d for c,d in entries)
       struct.pack_into('>I',data,ta+8+(n-1)*8+4,delta+extra)

 f.seek(off+head);f.write(data)
`
				if log, err := exec.Command("python3", "-c", code, runtime, source).CombinedOutput(); err != nil {
					t.Fatalf("presentation fixture: %v %s", err, log)
				}
			}
			params := redactionParamsForTest(t, "mute", false)
			params["_source_sample_rate"] = 96000
			plan := params["_redaction_plan"].(*audioRedactionPlan)
			plan.Ranges = []audioInterval{{StartMs: 1000, EndMs: 2000}}
			probe, err := runProbe(context.Background(), "ffprobe", source)
			if err != nil {
				t.Fatal(err)
			}
			offset, err := sourceAudioOffset(&MediaRow{HasVideo: true, RawProbe: json.RawMessage(probe.Raw)})
			if err != nil {
				t.Fatal(err)
			}
			plan.AudioStartMs = offset
			for _, remote := range []bool{false, true} {
				_, result, err := runGuardedFixture(t, source, "redact_audio", params, remote, "redacted.mov")
				if err != nil {
					t.Fatalf("remote=%v: %v %v", remote, err, result)
				}
				d := result["audio_redaction"].(map[string]any)
				if d["validated"] != true || d["gain_changed"] != false || d["sample_rate"] != float64(96000) || d["audio_duration_drift_ms"].(float64) > 1 {
					t.Fatalf("invalid extent evidence: %v", d)
				}
				if kind == "aac_padding_96k" && d["expected_audio_samples"] != float64(294720) {
					t.Fatalf("wrong sample extent: %v", d)
				}
				if kind == "video_longer" && d["expected_audio_samples"] != float64(288000) {
					t.Fatal("audio was padded to the video endpoint")
				}
				if d["video_pictures_match"] != true {
					t.Fatal("video changed")
				}
				wire, _ := json.Marshal(map[string]any{"audio_redaction": d})
				compact := compactRenderParams(wire)
				if compact["audio_redaction"].(map[string]any)["expected_audio_samples"] == nil {
					t.Fatal("sample diagnostics missing from API receipt")
				}
			}
		})
	}
}
