import { useEffect, useMemo, useRef, useState } from "react";
import {
  Archive,
  ArrowDown,
  ArrowUp,
  Check,
  FolderOpen,
  ImagePlus,
  LoaderCircle,
  Plus,
  Save,
  Send,
  Trash2,
  X,
} from "lucide-react";

const API = "/api/apps/creators";
const STORAGE_API = "/api/apps/storage";
const MAX_COVER_BYTES = 20 * 1024 * 1024;

function qs(projectId: string, extra: Record<string, unknown> = {}) {
  const params = new URLSearchParams();
  if (projectId) params.set("project_id", projectId);
  for (const [key, value] of Object.entries(extra)) {
    if (value !== undefined && value !== null && value !== "") {
      params.set(key, String(value));
    }
  }
  const query = params.toString();
  return query ? `?${query}` : "";
}

async function api(path: string, projectId: string, options: any = {}) {
  const query = qs(projectId, options.query || {});
  const url = `${API}${path}${path.includes("?") && query ? query.replace("?", "&") : query}`;
  const { query: _query, ...fetchOptions } = options;
  const response = await fetch(url, {
    credentials: "same-origin",
    headers: fetchOptions.body ? { "Content-Type": "application/json" } : undefined,
    ...fetchOptions,
    body: fetchOptions.body ? JSON.stringify(fetchOptions.body) : undefined,
  });
  if (!response.ok) {
    let message = `${response.status}`;
    try {
      const body = await response.json();
      message = body.error || message;
    } catch {
      // The status code remains useful for non-JSON proxy errors.
    }
    throw new Error(message);
  }
  return response.json();
}

function storageFileURL(fileId: number, projectId: string) {
  return `${STORAGE_API}/files/${fileId}/content${qs(projectId)}`;
}

async function uploadCoverFile(file: File, projectId: string, folder: string) {
  const form = new FormData();
  form.append("file", file);
  form.append("folder", folder);
  form.append("visibility", "private");
  const response = await fetch(`${STORAGE_API}/files${qs(projectId)}`, {
    method: "POST",
    credentials: "same-origin",
    body: form,
  });
  if (!response.ok) {
    throw new Error(`Cover upload failed: ${await response.text()}`);
  }
  const payload = await response.json();
  const uploaded = payload.file || payload;
  const id = Number(uploaded.id || 0);
  if (!id) {
    throw new Error("Storage did not return a file ID");
  }
  return id;
}

const emptyCollectionDraft = {
  title: "",
  slug: "",
  description: "",
  status: "draft",
  cover_storage_file_id: "",
  metadata: "{}",
  sort_order: 0,
};

function CreatorsPanel({ projectId }: { projectId: string }) {
  const loadSequence = useRef(0);
  const [tab, setTab] = useState("posts");
  const [spaces, setSpaces] = useState<any[]>([]);
  const [selectedSpaceId, setSelectedSpaceId] = useState<number | null>(null);
  const [space, setSpace] = useState<any>(null);
  const [tiers, setTiers] = useState<any[]>([]);
  const [members, setMembers] = useState<any[]>([]);
  const [posts, setPosts] = useState<any[]>([]);
  const [collections, setCollections] = useState<any[]>([]);
  const [events, setEvents] = useState<any[]>([]);
  const [revenue, setRevenue] = useState<any>({ mrr_by_currency: {} });
  const [status, setStatus] = useState("");
  const [draft, setDraft] = useState({ title: "", body: "", visibility: "members" });
  const [tierDraft, setTierDraft] = useState({
    name: "",
    price_cents: 500,
    currency: "USD",
    interval: "month",
  });
  const [memberDraft, setMemberDraft] = useState({
    email: "",
    display_name: "",
    status: "lead",
  });
  const [spaceDraft, setSpaceDraft] = useState({ name: "", slug: "" });
  const [collectionDraft, setCollectionDraft] = useState({ ...emptyCollectionDraft });
  const [selectedCollectionId, setSelectedCollectionId] = useState<number | null>(null);
  const [collectionEditor, setCollectionEditor] = useState<any>(null);
  const [orderedPostIds, setOrderedPostIds] = useState<number[]>([]);
  const [coverUploading, setCoverUploading] = useState(false);
  const coverInputRef = useRef<HTMLInputElement | null>(null);

  const spaceQuery = selectedSpaceId ? { space_id: selectedSpaceId } : {};

  const load = async () => {
    const sequence = ++loadSequence.current;
    try {
      const current = await api("/space", projectId, { query: spaceQuery });
      const spaceId = current.space?.id;
      if (spaceId && spaceId !== selectedSpaceId) setSelectedSpaceId(spaceId);
      const query = spaceId ? { space_id: spaceId } : {};
      const [allSpaces, tierData, memberData, postData, collectionData, eventData, metrics] =
        await Promise.all([
          api("/spaces", projectId),
          api("/tiers", projectId, { query }),
          api("/members", projectId, { query }),
          api("/posts", projectId, { query }),
          api("/collections", projectId, { query }),
          api("/activity", projectId, { query }),
          api("/metrics", projectId, { query }),
        ]);
      if (sequence !== loadSequence.current) return;
      setSpace(current.space);
      setSpaces(allSpaces.spaces || []);
      setTiers(tierData || []);
      setMembers(memberData || []);
      setPosts(postData || []);
      setCollections(collectionData.collections || []);
      setEvents(eventData || []);
      setRevenue(metrics || { mrr_by_currency: {} });
      setStatus("");
    } catch (error: any) {
      if (sequence !== loadSequence.current) return;
      setStatus(error.message || "Failed to load");
    }
  };

  useEffect(() => {
    load();
  }, [projectId, selectedSpaceId]);

  useEffect(() => {
    if (selectedCollectionId && !collections.some((item) => item.id === selectedCollectionId)) {
      setSelectedCollectionId(null);
      setCollectionEditor(null);
      setOrderedPostIds([]);
    }
  }, [collections, selectedCollectionId]);

  const metrics = useMemo(() => {
    const active = members.filter((member) => member.status === "active" || member.status === "comped").length;
    const published = posts.filter((post) => post.status === "published").length;
    return { active, published };
  }, [members, posts]);

  const mrr =
    Object.entries(revenue.mrr_by_currency || {})
      .map(([currency, cents]) => money(cents, currency as string))
      .join(" + ") || money(0, space?.default_currency || "USD");

  const run = async (operation: () => Promise<void>) => {
    try {
      setStatus("");
      await operation();
    } catch (error: any) {
      setStatus(error.message || "Request failed");
    }
  };

  const createPost = (event: any) => {
    event.preventDefault();
    if (!draft.title.trim()) return;
    run(async () => {
      await api("/posts", projectId, { method: "POST", query: spaceQuery, body: draft });
      setDraft({ title: "", body: "", visibility: "members" });
      await load();
    });
  };

  const createTier = (event: any) => {
    event.preventDefault();
    if (!tierDraft.name.trim()) return;
    run(async () => {
      await api("/tiers", projectId, {
        method: "POST",
        query: spaceQuery,
        body: { ...tierDraft, price_cents: Number(tierDraft.price_cents || 0) },
      });
      setTierDraft({
        name: "",
        price_cents: 500,
        currency: space?.default_currency || "USD",
        interval: "month",
      });
      await load();
    });
  };

  const createMember = (event: any) => {
    event.preventDefault();
    if (!memberDraft.email.trim()) return;
    run(async () => {
      await api("/members", projectId, {
        method: "POST",
        query: spaceQuery,
        body: memberDraft,
      });
      setMemberDraft({ email: "", display_name: "", status: "lead" });
      await load();
    });
  };

  const publish = (id: number) =>
    run(async () => {
      await api(`/posts/${id}/publish`, projectId, {
        method: "POST",
        query: spaceQuery,
        body: {},
      });
      await load();
    });

  const createSpace = (event: any) => {
    event.preventDefault();
    if (!spaceDraft.name.trim()) return;
    run(async () => {
      const response = await api("/spaces", projectId, { method: "POST", body: spaceDraft });
      setSpaceDraft({ name: "", slug: "" });
      if (response.space?.id) setSelectedSpaceId(response.space.id);
    });
  };

  const createCollection = (event: any) => {
    event.preventDefault();
    if (!collectionDraft.title.trim()) return;
    run(async () => {
      const body = collectionBody(collectionDraft);
      const response = await api("/collections", projectId, {
        method: "POST",
        query: spaceQuery,
        body,
      });
      setCollectionDraft({ ...emptyCollectionDraft });
      await load();
      await openCollection(response.collection.id);
    });
  };

  const openCollection = async (id: number) => {
    await run(async () => {
      const response = await api(`/collections/${id}`, projectId, { query: spaceQuery });
      setSelectedCollectionId(id);
      setCollectionEditor({
        ...response.collection,
        cover_storage_file_id: response.collection.cover_storage_file_id || "",
        metadata: JSON.stringify(response.collection.metadata || {}, null, 2),
      });
      setOrderedPostIds((response.posts || []).map((item: any) => item.post.id));
    });
  };

  const saveCollection = () =>
    run(async () => {
      await api(`/collections/${selectedCollectionId}`, projectId, {
        method: "PATCH",
        query: spaceQuery,
        body: collectionBody(collectionEditor),
      });
      await api(`/collections/${selectedCollectionId}/posts`, projectId, {
        method: "PUT",
        query: spaceQuery,
        body: { post_ids: orderedPostIds },
      });
      await load();
      await openCollection(selectedCollectionId as number);
    });

  const archiveSelectedCollection = () =>
    run(async () => {
      await api(`/collections/${selectedCollectionId}`, projectId, {
        method: "DELETE",
        query: spaceQuery,
      });
      setSelectedCollectionId(null);
      setCollectionEditor(null);
      setOrderedPostIds([]);
      await load();
    });

  const uploadCollectionCover = async (event: any) => {
    const file = event.target.files?.[0] as File | undefined;
    event.target.value = "";
    if (!file || !selectedCollectionId) return;
    if (!file.type.startsWith("image/")) {
      setStatus("Collection cover must be an image.");
      return;
    }
    if (file.size > MAX_COVER_BYTES) {
      setStatus("Collection cover must be 20 MB or smaller.");
      return;
    }
    setCoverUploading(true);
    setStatus("");
    try {
      const folder = `/creators/${space?.slug || "creator"}/collections/`;
      const fileId = await uploadCoverFile(file, projectId, folder);
      await api(`/collections/${selectedCollectionId}`, projectId, {
        method: "PATCH",
        query: spaceQuery,
        body: { cover_storage_file_id: fileId },
      });
      setCollectionEditor((current: any) => ({
        ...current,
        cover_storage_file_id: fileId,
      }));
      await load();
      setStatus("Collection cover updated.");
    } catch (error: any) {
      setStatus(error.message || "Cover upload failed");
    } finally {
      setCoverUploading(false);
    }
  };

  const removeCollectionCover = () =>
    run(async () => {
      if (!selectedCollectionId) return;
      setCoverUploading(true);
      try {
        await api(`/collections/${selectedCollectionId}`, projectId, {
          method: "PATCH",
          query: spaceQuery,
          body: { cover_storage_file_id: 0 },
        });
        setCollectionEditor((current: any) => ({
          ...current,
          cover_storage_file_id: "",
        }));
        await load();
        setStatus("Collection cover removed.");
      } finally {
        setCoverUploading(false);
      }
    });

  const toggleCollectionPost = (id: number) => {
    setOrderedPostIds((current) =>
      current.includes(id) ? current.filter((postId) => postId !== id) : [...current, id],
    );
  };

  const moveCollectionPost = (id: number, delta: number) => {
    setOrderedPostIds((current) => {
      const index = current.indexOf(id);
      const nextIndex = index + delta;
      if (index < 0 || nextIndex < 0 || nextIndex >= current.length) return current;
      const next = [...current];
      [next[index], next[nextIndex]] = [next[nextIndex], next[index]];
      return next;
    });
  };

  return (
    <div className="h-full overflow-auto bg-bg text-text">
      <header className="border-b border-border px-5 py-4 flex flex-wrap items-center gap-4">
        <div className="min-w-0">
          <h1 className="text-lg font-semibold">{space?.name || "Creators"}</h1>
          <p className="text-xs text-text-muted truncate">
            {space?.description || "Memberships, collections, gated posts, and supporter files."}
          </p>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <select
            value={selectedSpaceId || ""}
            onChange={(event) => setSelectedSpaceId(Number(event.target.value))}
            className={input}
            aria-label="Creator space"
          >
            {spaces.map((item) => (
              <option value={item.id} key={item.id}>
                {item.name}
              </option>
            ))}
          </select>
          <form onSubmit={createSpace} className="flex items-center gap-2">
            <input
              value={spaceDraft.name}
              onChange={(event) => setSpaceDraft({ ...spaceDraft, name: event.target.value })}
              placeholder="New space"
              className={`${input} w-32`}
            />
            <button className={iconButton} title="Create space" aria-label="Create space">
              <Plus size={16} />
            </button>
          </form>
        </div>
        <div className="ml-auto grid grid-cols-2 sm:grid-cols-4 gap-2 text-right">
          {metric("Members", metrics.active)}
          {metric("Posts", metrics.published)}
          {metric("Collections", collections.length)}
          {metric("MRR", mrr)}
        </div>
      </header>

      <nav className="px-5 py-3 border-b border-border flex gap-2 overflow-x-auto">
        {["posts", "collections", "tiers", "members", "events"].map((item) => (
          <button
            type="button"
            onClick={() => setTab(item)}
            className={`px-3 py-1.5 rounded text-sm whitespace-nowrap ${
              tab === item ? "bg-bg-card text-text" : "text-text-muted hover:text-text"
            }`}
            key={item}
          >
            {item[0].toUpperCase() + item.slice(1)}
          </button>
        ))}
      </nav>

      {status && (
        <div className="mx-5 mt-4 rounded border border-error/40 bg-error/10 px-3 py-2 text-sm text-error">
          {status}
        </div>
      )}

      {tab === "posts" && (
        <main className="p-5 grid lg:grid-cols-[minmax(0,1fr)_360px] gap-5">
          <section className="space-y-2">
            {posts.length === 0
              ? empty("No posts yet.")
              : posts.map((post) => (
                  <PostRow post={post} onPublish={() => publish(post.id)} collections={collections} key={post.id} />
                ))}
          </section>
          <form onSubmit={createPost} className={sidePanel}>
            <h2 className="font-medium text-sm">New Post</h2>
            <input
              value={draft.title}
              onChange={(event) => setDraft({ ...draft, title: event.target.value })}
              placeholder="Title"
              className={input}
            />
            <textarea
              value={draft.body}
              onChange={(event) => setDraft({ ...draft, body: event.target.value })}
              placeholder="Body"
              className={`${input} min-h-32`}
            />
            <select
              value={draft.visibility}
              onChange={(event) => setDraft({ ...draft, visibility: event.target.value })}
              className={input}
            >
              {["public", "members", "tier", "private"].map((value) => (
                <option value={value} key={value}>
                  {value}
                </option>
              ))}
            </select>
            <button className={primary}>
              <Plus size={16} />
              Create post
            </button>
          </form>
        </main>
      )}

      {tab === "collections" && (
        <main className="p-5 grid xl:grid-cols-[300px_minmax(0,1fr)] gap-5">
          <aside className="space-y-4 min-w-0">
            <section className="space-y-2">
              {collections.length === 0
                ? empty("No collections yet.")
                : collections.map((collection) => (
                    <button
                      type="button"
                      onClick={() => openCollection(collection.id)}
                      className={`w-full border rounded px-3 py-3 text-left flex items-center gap-3 ${
                        selectedCollectionId === collection.id
                          ? "border-accent bg-bg-card"
                          : "border-border bg-bg-card hover:border-text-muted"
                      }`}
                      key={collection.id}
                    >
                      <FolderOpen size={17} className="shrink-0 text-text-muted" />
                      <span className="min-w-0 flex-1">
                        <span className="block font-medium truncate">{collection.title}</span>
                        <span className="block text-xs text-text-muted">
                          {collection.status} · {collection.post_count} posts
                        </span>
                      </span>
                    </button>
                  ))}
            </section>
            <form onSubmit={createCollection} className={sidePanel}>
              <h2 className="font-medium text-sm">New Collection</h2>
              <input
                value={collectionDraft.title}
                onChange={(event) => setCollectionDraft({ ...collectionDraft, title: event.target.value })}
                placeholder="Title"
                className={input}
              />
              <textarea
                value={collectionDraft.description}
                onChange={(event) =>
                  setCollectionDraft({ ...collectionDraft, description: event.target.value })
                }
                placeholder="Description"
                className={`${input} min-h-20`}
              />
              <button className={primary}>
                <Plus size={16} />
                Create collection
              </button>
            </form>
          </aside>

          {!collectionEditor ? (
            <section className="border border-border rounded min-h-72 grid place-items-center text-sm text-text-muted">
              Select a collection
            </section>
          ) : (
            <section className="min-w-0 space-y-5">
              <div className="border-b border-border pb-4 flex flex-wrap items-center gap-3">
                <div className="min-w-0 flex-1">
                  <h2 className="font-semibold truncate">{collectionEditor.title}</h2>
                  <div className="text-xs text-text-muted">/{collectionEditor.slug}</div>
                </div>
                <button type="button" onClick={archiveSelectedCollection} className={secondary}>
                  <Archive size={16} />
                  Archive
                </button>
                <button type="button" onClick={saveCollection} disabled={coverUploading} className={primary}>
                  <Save size={16} />
                  Save
                </button>
              </div>

              <div className="grid md:grid-cols-2 gap-4">
                <label className={fieldLabel}>
                  <span>Title</span>
                  <input
                    value={collectionEditor.title}
                    onChange={(event) =>
                      setCollectionEditor({ ...collectionEditor, title: event.target.value })
                    }
                    className={input}
                  />
                </label>
                <label className={fieldLabel}>
                  <span>Slug</span>
                  <input
                    value={collectionEditor.slug}
                    onChange={(event) =>
                      setCollectionEditor({ ...collectionEditor, slug: event.target.value })
                    }
                    className={input}
                  />
                </label>
                <label className={fieldLabel}>
                  <span>Status</span>
                  <select
                    value={collectionEditor.status}
                    onChange={(event) =>
                      setCollectionEditor({ ...collectionEditor, status: event.target.value })
                    }
                    className={input}
                  >
                    {["draft", "published", "archived"].map((value) => (
                      <option value={value} key={value}>
                        {value}
                      </option>
                    ))}
                  </select>
                </label>
                <div className={`${fieldLabel} md:col-span-2`}>
                  <span>Cover image</span>
                  <div className="border border-border rounded overflow-hidden bg-bg-input">
                    <div className="aspect-[3/1] min-h-36 max-h-64 grid place-items-center overflow-hidden">
                      {collectionEditor.cover_storage_file_id ? (
                        <img
                          src={storageFileURL(Number(collectionEditor.cover_storage_file_id), projectId)}
                          alt={`${collectionEditor.title} cover`}
                          className="w-full h-full object-cover"
                        />
                      ) : (
                        <ImagePlus size={30} className="text-text-muted" />
                      )}
                    </div>
                    <div className="border-t border-border px-3 py-2 flex flex-wrap items-center gap-2">
                      <button
                        type="button"
                        onClick={() => coverInputRef.current?.click()}
                        disabled={coverUploading}
                        className={secondary}
                      >
                        {coverUploading ? (
                          <LoaderCircle size={16} className="animate-spin" />
                        ) : (
                          <ImagePlus size={16} />
                        )}
                        {coverUploading
                          ? "Uploading"
                          : collectionEditor.cover_storage_file_id
                            ? "Replace cover"
                            : "Upload cover"}
                      </button>
                      <input
                        ref={coverInputRef}
                        type="file"
                        accept="image/*"
                        onChange={uploadCollectionCover}
                        className="hidden"
                      />
                      {collectionEditor.cover_storage_file_id && (
                        <button
                          type="button"
                          onClick={removeCollectionCover}
                          disabled={coverUploading}
                          className={secondary}
                        >
                          <Trash2 size={16} />
                          Remove
                        </button>
                      )}
                      {collectionEditor.cover_storage_file_id && (
                        <span className="ml-auto text-xs text-text-muted">
                          Storage #{collectionEditor.cover_storage_file_id}
                        </span>
                      )}
                    </div>
                  </div>
                </div>
                <label className={`${fieldLabel} md:col-span-2`}>
                  <span>Description</span>
                  <textarea
                    value={collectionEditor.description}
                    onChange={(event) =>
                      setCollectionEditor({ ...collectionEditor, description: event.target.value })
                    }
                    className={`${input} min-h-24`}
                  />
                </label>
                <label className={`${fieldLabel} md:col-span-2`}>
                  <span>Metadata</span>
                  <textarea
                    value={collectionEditor.metadata}
                    onChange={(event) =>
                      setCollectionEditor({ ...collectionEditor, metadata: event.target.value })
                    }
                    spellCheck={false}
                    className={`${input} min-h-28 font-mono text-xs`}
                  />
                </label>
              </div>

              <div>
                <div className="flex items-center justify-between gap-3 mb-2">
                  <h3 className="font-medium text-sm">Post Order</h3>
                  <span className="text-xs text-text-muted">{orderedPostIds.length} selected</span>
                </div>
                <div className="border border-border rounded divide-y divide-border">
                  {orderedPostIds.length === 0
                    ? emptyInline("No posts selected.")
                    : orderedPostIds.map((postId, index) => {
                        const post = posts.find((item) => item.id === postId);
                        if (!post) return null;
                        return (
                          <div className="px-3 py-2 flex items-center gap-2" key={postId}>
                            <span className="w-7 text-xs text-text-muted tabular-nums">{index + 1}</span>
                            <span className="min-w-0 flex-1">
                              <span className="block text-sm font-medium truncate">{post.title}</span>
                              <span className="block text-xs text-text-muted">
                                {post.status} · {post.visibility}
                              </span>
                            </span>
                            <button
                              type="button"
                              onClick={() => moveCollectionPost(postId, -1)}
                              disabled={index === 0}
                              className={smallIcon}
                              title="Move up"
                              aria-label={`Move ${post.title} up`}
                            >
                              <ArrowUp size={15} />
                            </button>
                            <button
                              type="button"
                              onClick={() => moveCollectionPost(postId, 1)}
                              disabled={index === orderedPostIds.length - 1}
                              className={smallIcon}
                              title="Move down"
                              aria-label={`Move ${post.title} down`}
                            >
                              <ArrowDown size={15} />
                            </button>
                            <button
                              type="button"
                              onClick={() => toggleCollectionPost(postId)}
                              className={smallIcon}
                              title="Remove post"
                              aria-label={`Remove ${post.title}`}
                            >
                              <X size={15} />
                            </button>
                          </div>
                        );
                      })}
                </div>
              </div>

              <div>
                <h3 className="font-medium text-sm mb-2">Post Library</h3>
                <div className="border border-border rounded divide-y divide-border max-h-80 overflow-auto">
                  {posts.length === 0
                    ? emptyInline("No posts available.")
                    : posts.map((post) => {
                        const selected = orderedPostIds.includes(post.id);
                        return (
                          <button
                            type="button"
                            onClick={() => toggleCollectionPost(post.id)}
                            className="w-full px-3 py-2 text-left flex items-center gap-3 hover:bg-bg-card"
                            key={post.id}
                          >
                            <span
                              className={`w-5 h-5 border rounded grid place-items-center shrink-0 ${
                                selected ? "border-accent bg-accent text-bg" : "border-border"
                              }`}
                            >
                              {selected && <Check size={14} />}
                            </span>
                            <span className="min-w-0 flex-1">
                              <span className="block text-sm font-medium truncate">{post.title}</span>
                              <span className="block text-xs text-text-muted">
                                {post.status} · {post.visibility}
                              </span>
                            </span>
                          </button>
                        );
                      })}
                </div>
              </div>
            </section>
          )}
        </main>
      )}

      {tab === "tiers" && (
        <main className="p-5 grid lg:grid-cols-[minmax(0,1fr)_360px] gap-5">
          <section className="space-y-2">
            {tiers.length === 0
              ? empty("No tiers yet.")
              : tiers.map((tier) => <TierRow tier={tier} key={tier.id} />)}
          </section>
          <form onSubmit={createTier} className={sidePanel}>
            <h2 className="font-medium text-sm">New Tier</h2>
            <input
              value={tierDraft.name}
              onChange={(event) => setTierDraft({ ...tierDraft, name: event.target.value })}
              placeholder="Name"
              className={input}
            />
            <input
              type="number"
              value={tierDraft.price_cents}
              onChange={(event) => setTierDraft({ ...tierDraft, price_cents: event.target.value as any })}
              className={input}
            />
            <select
              value={tierDraft.interval}
              onChange={(event) => setTierDraft({ ...tierDraft, interval: event.target.value })}
              className={input}
            >
              {["month", "year", "one_time"].map((value) => (
                <option value={value} key={value}>
                  {value}
                </option>
              ))}
            </select>
            <button className={primary}>
              <Plus size={16} />
              Create tier
            </button>
          </form>
        </main>
      )}

      {tab === "members" && (
        <main className="p-5 grid lg:grid-cols-[minmax(0,1fr)_360px] gap-5">
          <section className="space-y-2">
            {members.length === 0
              ? empty("No members yet.")
              : members.map((member) => <MemberRow member={member} tiers={tiers} key={member.id} />)}
          </section>
          <form onSubmit={createMember} className={sidePanel}>
            <h2 className="font-medium text-sm">New Member</h2>
            <input
              value={memberDraft.email}
              onChange={(event) => setMemberDraft({ ...memberDraft, email: event.target.value })}
              placeholder="email@example.com"
              className={input}
            />
            <input
              value={memberDraft.display_name}
              onChange={(event) => setMemberDraft({ ...memberDraft, display_name: event.target.value })}
              placeholder="Display name"
              className={input}
            />
            <select
              value={memberDraft.status}
              onChange={(event) => setMemberDraft({ ...memberDraft, status: event.target.value })}
              className={input}
            >
              {["lead", "active", "past_due", "paused", "cancelled", "comped"].map((value) => (
                <option value={value} key={value}>
                  {value}
                </option>
              ))}
            </select>
            <button className={primary}>
              <Plus size={16} />
              Create member
            </button>
          </form>
        </main>
      )}

      {tab === "events" && (
        <main className="p-5 space-y-2">
          {events.length === 0
            ? empty("No events yet.")
            : events.map((event) => (
                <div className="border border-border rounded bg-bg-card px-3 py-2 text-sm" key={event.id}>
                  <div className="flex gap-2">
                    <span className="font-medium">{event.kind}</span>
                    <span className="text-text-muted">{event.created_at}</span>
                  </div>
                  <pre className="text-xs text-text-muted whitespace-pre-wrap mt-1">
                    {JSON.stringify(event.data || {}, null, 2)}
                  </pre>
                </div>
              ))}
        </main>
      )}
    </div>
  );
}

function collectionBody(draft: any) {
  let metadata;
  try {
    metadata = JSON.parse(draft.metadata || "{}");
  } catch {
    throw new Error("Collection metadata must be valid JSON");
  }
  if (!metadata || Array.isArray(metadata) || typeof metadata !== "object") {
    throw new Error("Collection metadata must be a JSON object");
  }
  return {
    title: draft.title,
    slug: draft.slug || undefined,
    description: draft.description,
    status: draft.status,
    cover_storage_file_id: draft.cover_storage_file_id
      ? Number(draft.cover_storage_file_id)
      : null,
    metadata,
    sort_order: Number(draft.sort_order || 0),
  };
}

function metric(label: string, value: any) {
  return (
    <div className="border border-border rounded bg-bg-card px-3 py-2 min-w-20">
      <div className="text-[10px] uppercase text-text-muted">{label}</div>
      <div className="font-semibold whitespace-nowrap">{value}</div>
    </div>
  );
}

function PostRow({ post, onPublish, collections }: any) {
  const names = (post.collection_ids || [])
    .map((id: number) => collections.find((collection: any) => collection.id === id)?.title)
    .filter(Boolean);
  return (
    <div className="border border-border rounded bg-bg-card px-3 py-3">
      <div className="flex gap-3 items-start">
        <div className="min-w-0 flex-1">
          <div className="font-medium truncate">{post.title}</div>
          <div className="text-xs text-text-muted">
            {post.status} · {post.visibility} · /{post.slug}
          </div>
          {names.length > 0 && <div className="text-xs text-text-muted mt-1 truncate">{names.join(" · ")}</div>}
        </div>
        {post.status !== "published" && (
          <button type="button" onClick={onPublish} className={secondary}>
            <Send size={15} />
            Publish
          </button>
        )}
      </div>
      {post.body && <p className="text-sm text-text-muted mt-2 line-clamp-2">{post.body}</p>}
    </div>
  );
}

function TierRow({ tier }: any) {
  return (
    <div className="border border-border rounded bg-bg-card px-3 py-3 flex items-center gap-3">
      <div className="font-medium flex-1">{tier.name}</div>
      <div className="text-sm text-text-muted">
        {money(tier.price_cents, tier.currency)} / {tier.interval}
      </div>
    </div>
  );
}

function MemberRow({ member, tiers }: any) {
  const tier = tiers.find((item: any) => item.id === member.tier_id);
  return (
    <div className="border border-border rounded bg-bg-card px-3 py-3 flex items-center gap-3">
      <div className="min-w-0 flex-1">
        <div className="font-medium truncate">{member.display_name || member.email}</div>
        <div className="text-xs text-text-muted truncate">{member.email}</div>
      </div>
      <div className="text-sm text-text-muted">{tier?.name || "No tier"}</div>
      <div className="text-xs border border-border rounded px-2 py-1">{member.status}</div>
    </div>
  );
}

function empty(text: string) {
  return (
    <div className="border border-border rounded bg-bg-card p-8 text-center text-sm text-text-muted">
      {text}
    </div>
  );
}

function emptyInline(text: string) {
  return <div className="p-6 text-center text-sm text-text-muted">{text}</div>;
}

function money(cents: any, currency: string) {
  return new Intl.NumberFormat(undefined, {
    style: "currency",
    currency: currency || "USD",
  }).format((Number(cents) || 0) / 100);
}

const input =
  "w-full bg-bg-input border border-border rounded px-3 py-2 text-sm min-h-9";
const primary =
  "inline-flex items-center justify-center gap-2 bg-accent text-bg rounded px-3 py-2 text-sm font-medium disabled:opacity-50";
const secondary =
  "inline-flex items-center justify-center gap-2 border border-border rounded px-3 py-2 text-sm hover:border-accent disabled:opacity-50";
const iconButton =
  "w-9 h-9 grid place-items-center border border-border rounded hover:border-accent disabled:opacity-50";
const smallIcon =
  "w-8 h-8 grid place-items-center border border-border rounded hover:border-accent disabled:opacity-30";
const sidePanel =
  "border border-border rounded bg-bg-card p-4 flex flex-col gap-3 h-fit";
const fieldLabel = "flex flex-col gap-1.5 text-xs text-text-muted";

export default CreatorsPanel;
