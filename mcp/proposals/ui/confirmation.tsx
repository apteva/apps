import {useEffect, useId, useRef, useState} from "react";
import {Dialog} from "./backlog";

type Confirmation = {message: string; title?: string; action?: string};
export function useConfirmation() {
  const [pending, setPending] = useState<Confirmation>();
  const resolve = useRef<((confirmed: boolean) => void) | undefined>(undefined);
  const description = useId();
  useEffect(() => () => { resolve.current?.(false); resolve.current = undefined; }, []);
  const finish = (confirmed: boolean) => {
    resolve.current?.(confirmed);
    resolve.current = undefined;
    setPending(undefined);
  };
  const confirm = (message: string, action = "Continue", title = "Confirm action") => new Promise<boolean>((done) => {
    // Never stack confirmations or leave an earlier action waiting.
    resolve.current?.(false);
    resolve.current = done;
    setPending({message, title, action});
  });
  const modal = pending && <Dialog title={pending.title!} onClose={() => finish(false)} description={description} compact>
    <div className="pp-dialog-body">
      <p id={description} className="pp-copy pp-muted">{pending.message}</p>
      <footer><button type="button" autoFocus onClick={() => finish(false)}>Cancel</button>
        <button type="button" className="pp-primary" onClick={() => finish(true)}>{pending.action}</button></footer>
    </div>
  </Dialog>;
  return {confirm, modal};
}
