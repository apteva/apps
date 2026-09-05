export function reportSectionsText(value: unknown): string {
  if (!Array.isArray(value)) return "";
  return value.map(section => {
    if (!section || typeof section !== "object") return String(section ?? "");
    const {title, heading, body, text, content, ...rest} = section as Record<string,unknown>;
    return [title || heading ? `## ${String(title || heading)}` : "", String(body ?? text ?? content ?? ""), Object.keys(rest).length ? JSON.stringify(rest,null,2) : ""].filter(Boolean).join("\n\n");
  }).filter(Boolean).join("\n\n");
}
export function AttachmentContent({attachments=[]}: {attachments?: Array<{type:string;data_url?:string;name?:string}>}) {
  return <>{attachments.map((attachment,index) => attachment.type === "image" && /^data:image\/(png|jpeg|gif|webp);base64,/i.test(attachment.data_url ?? "")
    ? <img key={index} src={attachment.data_url} alt={attachment.name || "Image attachment"} loading="lazy" className="max-h-96 max-w-full rounded border border-border" />
    : <p key={index} role="status">Attachment cannot be displayed: {attachment.name || attachment.type}</p>)}</>;
}
export function GenericComponents({components=[]}: {components?:Array<{app:string;name:string;props:Record<string,unknown>}>}) {
 return <>{components.filter(c => !["approval-card","report-card","alert-card"].includes(c.name)).map((c,i) => <details key={i} className="rounded border border-border p-2"><summary>{c.app}: {c.name}</summary><pre className="whitespace-pre-wrap break-words text-xs">{JSON.stringify(c.props,null,2)}</pre></details>)}</>;
}
