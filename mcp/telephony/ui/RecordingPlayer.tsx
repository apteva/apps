import {useState} from "react";

export interface RecordingPlayback {
  playback_url?: string;
  playback_source?: "storage" | "provider" | "local";
  playback_urls?: Partial<Record<"mix" | "caller" | "agent" | "original",string>>;
}

export default function RecordingPlayer({ recording }: { recording: RecordingPlayback }) {
  const variants = recording.playback_urls ?? (recording.playback_url ? { mix: recording.playback_url } : {});
  const available = (["mix", "caller", "agent", "original"] as const).filter((variant) => Boolean(variants[variant]));
  const [variant, setVariant] = useState<(typeof available)[number]>(available[0] ?? "mix");
  const url = variants[variant] ?? recording.playback_url;
 const [playbackError,setPlaybackError]=useState("");
  if (!url) return null;
  const labels = { mix: "Balanced", caller: "Caller", agent: "Agent", original: "Original" };
  return (
    <div className="mt-3 space-y-2">
      {available.length > 1 ? (
        <div className="inline-flex max-w-full overflow-x-auto rounded border border-border p-0.5" role="group" aria-label="Recording track">
          {available.map((item) => (
            <button
              key={item}
              type="button"
              onClick={() => {setVariant(item);setPlaybackError("");}}
              className={`h-7 whitespace-nowrap px-2 text-xs ${variant === item ? "bg-bg-muted font-medium text-text" : "text-text-muted hover:text-text"}`}
            >
              {labels[item]}
            </button>
          ))}
        </div>
      ) : null}
      <div className="flex items-center gap-3">
        <audio key={url} src={url} controls preload="metadata" className="min-w-0 flex-1 h-9" onError={()=>{
          if(variant==="mix"&&variants.original){setVariant("original");setPlaybackError("Balanced playback unavailable. Playing the original channels.");}
          else setPlaybackError(variant==="caller"||variant==="agent" ? `${labels[variant]}-only playback unavailable. Select Original to play both sides.` : "Recording could not be played.");
        }} />
        <a href={url} download className="text-xs text-accent hover:underline">Download</a>
        <span className="text-xs text-text-dim">{recording.playback_source === "storage" ? "Storage" : recording.playback_source === "local" ? "Local recording" : "Carrier"}</span>
      </div>
      {playbackError ? <p role="status" className="text-xs text-warn">{playbackError}</p> : null}
      {variant==="mix" ? <p className="text-xs text-text-dim">Uses the original channels if balanced playback cannot be generated.</p> : null}
    </div>
  );
}

