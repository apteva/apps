import { useCallback, useEffect, useState } from "react";
import { Megaphone } from "lucide-react";
import { Card, CardHeader, type CardVendor } from "@apteva/ui-kit";
import { platformPresentation } from "./platformPresentation";
import { postLifecycleDate } from "./postCalendar";
import {
  fetchSocialPost,
  isPostLifecycleEvent,
  postStatusColor,
  postStatusVariant,
  postTitle,
  previewSinglePost,
  subscribeSocialEvents,
  type SocialPost,
  type SocialPostTarget,
} from "./socialCardData";

interface Props {
  post_id: number;
  projectId?: string;
  preview?: boolean;
}

const socialVendor: CardVendor = {
  name: "Social",
  logo: <Megaphone size={14} aria-hidden />,
  color: { light: "#C2410C", dark: "#FB923C" },
};

export default function SocialPostCard({ post_id, projectId, preview }: Props) {
  const [post, setPost] = useState<SocialPost | null>(preview ? previewSinglePost : null);
  const [error, setError] = useState("");
  const load = useCallback(async () => {
    if (preview) return;
    try {
      setPost(await fetchSocialPost(post_id, projectId));
      setError("");
    } catch (cause) {
      setError((cause as Error).message || "Couldn't load post");
    }
  }, [post_id, preview, projectId]);

  useEffect(() => {
    void load();
    if (preview) return;
    const interval = window.setInterval(() => void load(), 60_000);
    return () => window.clearInterval(interval);
  }, [load, preview]);

  useEffect(() => {
    if (preview) return;
    return subscribeSocialEvents(projectId, (event) => {
      if (!isPostLifecycleEvent(event.topic)) return;
      const eventPostID = Number(event.data?.post_id || 0);
      if (event.topic.startsWith("post.") && eventPostID > 0 && eventPostID !== post_id) return;
      void load();
    });
  }, [load, post_id, preview, projectId]);

  if (error && !post) {
    return (
      <Card>
        <CardHeader vendor={socialVendor} title={`Post #${post_id}`} status={{ label: "unavailable", variant: "err" }} />
        <div className="px-4 py-4 text-xs text-red">{error}</div>
      </Card>
    );
  }
  if (!post) {
    return (
      <Card>
        <CardHeader vendor={socialVendor} title={`Post #${post_id}`} status={{ label: "loading", variant: "muted" }} />
      </Card>
    );
  }

  const date = postLifecycleDate(post);
  return (
    <Card>
      <CardHeader
        vendor={socialVendor}
        title={postTitle(post)}
        subtitle={date ? date.toLocaleString() : `Post #${post.id}`}
        status={{ label: post.status, variant: postStatusVariant(post.status) }}
        action={{ label: "Open post", href: `/apps/social?post=${post.id}` }}
      />

      <PostMedia post={post} projectId={projectId} />

      <div className="px-4 py-3 flex flex-col gap-3">
        <div
          className="text-sm text-text whitespace-pre-line leading-5"
          style={{ display: "-webkit-box", WebkitBoxOrient: "vertical", WebkitLineClamp: 4, overflow: "hidden" }}
        >
          {post.body || <span className="text-text-dim italic">No caption</span>}
        </div>

        <div className="flex flex-col border-t border-border">
          {post.targets.map((target) => <TargetRow key={target.id} target={target} />)}
          {post.targets.length === 0 && <div className="pt-3 text-xs text-text-dim">No destinations attached.</div>}
        </div>

        {error && <div className="text-[10px] text-warn">Live refresh failed: {error}</div>}
      </div>
    </Card>
  );
}

function TargetRow({ target }: { target: SocialPostTarget }) {
  const presentation = platformPresentation(target.platform);
  const content = (
    <>
      <span
        className="inline-grid place-items-center rounded font-bold flex-shrink-0"
        style={{
          width: 22,
          height: 22,
          fontSize: 9,
          color: presentation.color,
          backgroundColor: `${presentation.color}1F`,
          border: `1px solid ${presentation.color}88`,
        }}
      >
        {presentation.mark}
      </span>
      <span className="min-w-0 flex-1">
        <span className="block text-xs text-text truncate">{target.display_name || presentation.label}</span>
        <span className="block text-[10px] text-text-dim truncate">{presentation.label}</span>
      </span>
      <span className="text-[10px] uppercase" style={{ color: postStatusColor(target.status) }}>{target.status}</span>
    </>
  );
  const className = "flex items-center gap-2 py-2.5 border-b border-border last:border-b-0";
  return target.platform_url ? (
    <a href={target.platform_url} target="_blank" rel="noopener" className={`${className} hover:bg-bg-input/40`}>{content}</a>
  ) : (
    <div className={className}>{content}</div>
  );
}

function PostMedia({ post, projectId }: { post: SocialPost; projectId?: string }) {
  const storageID = post.media_storage_ids?.[0];
  const externalURL = !storageID ? post.external_media_urls?.[0] : "";
  if (storageID) return <StorageMedia fileID={storageID} projectId={projectId} />;
  if (externalURL) {
    return <img src={externalURL} alt="" loading="lazy" className="w-full h-40 object-cover bg-bg-input border-b border-border" />;
  }
  return null;
}

function StorageMedia({ fileID, projectId }: { fileID: number; projectId?: string }) {
  const [mime, setMime] = useState("");
  useEffect(() => {
    fetch(storageMetadataURL(fileID, projectId), { credentials: "same-origin" })
      .then((response) => response.ok ? response.json() : Promise.reject())
      .then((data) => setMime(data?.file?.content_type || data?.content_type || ""))
      .catch(() => setMime(""));
  }, [fileID, projectId]);
  const url = storageContentURL(fileID, projectId);
  if (mime.startsWith("video/")) {
    return <video src={url} preload="metadata" muted playsInline className="w-full h-40 object-cover bg-black border-b border-border" />;
  }
  if (mime.startsWith("image/")) {
    return <img src={url} alt="" loading="lazy" className="w-full h-40 object-cover bg-bg-input border-b border-border" />;
  }
  return (
    <div className="h-20 grid place-items-center bg-bg-input border-b border-border text-xs text-text-dim">
      Attached file #{fileID}
    </div>
  );
}

function storageMetadataURL(fileID: number, projectId?: string): string {
  const suffix = projectId ? `?project_id=${encodeURIComponent(projectId)}` : "";
  return `/api/apps/storage/files/${fileID}${suffix}`;
}

function storageContentURL(fileID: number, projectId?: string): string {
  const suffix = projectId ? `?project_id=${encodeURIComponent(projectId)}` : "";
  return `/api/apps/storage/files/${fileID}/content${suffix}`;
}
