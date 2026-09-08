import { useEffect, useMemo, useRef, useState } from "react";
import { AptevaClient } from "@apteva/web-sdk";
import { telephonyExtension } from "../frontend/src/client";
import type { HeadlessSoftphone, SoftphoneOptions, SoftphoneSnapshot } from "../frontend/src/softphone";

/** React lifecycle only. Every call operation lives in the app-owned controller. */
export function usePanelSoftphone(projectId: string, installId: number, options: SoftphoneOptions) {
  const callbacks = useRef(options);
  callbacks.current = options;
  const client = useMemo(() => new AptevaClient({ baseURL: "" }).use(telephonyExtension, { projectId, installId }), [projectId, installId]);
  const [phone, setPhone] = useState<HeadlessSoftphone>();
  const [state, setState] = useState<SoftphoneSnapshot>({ audioState: "idle", busy: false, muted: false });
  useEffect(() => {
    const controller = client.createSoftphone({
      audio: callbacks.current.audio,
      pollIntervalMs: 0, // CallsView already watches the call list.
      onLevels: (mic, speaker) => callbacks.current.onLevels?.(mic, speaker),
      onDiagnostics: value => callbacks.current.onDiagnostics?.(value),
      onNotice: value => callbacks.current.onNotice?.(value),
    });
    setPhone(controller);
    setState(controller.getSnapshot());
    const unsubscribe = controller.subscribe(setState);
    return () => { unsubscribe(); controller.dispose(); };
  }, [client]);
  useEffect(() => { phone?.configureAudio(options.audio ?? {}); }, [phone, options.audio]);
  return { client, phone, state };
}
