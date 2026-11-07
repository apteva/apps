import {createRoot} from "react-dom/client";
import RecordingPlayer from "../../ui/RecordingPlayer";
createRoot(document.getElementById("root")!).render(<RecordingPlayer recording={{playback_source:"local",playback_urls:{mix:"/audio/mix",caller:"/audio/caller",agent:"/audio/agent",original:"/audio/original"}}}/>);
