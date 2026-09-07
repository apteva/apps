export interface EditablePost {
  id: number;
  edit_version: number;
  title: string;
  excerpt?: string;
}
export type ContentAPI = <T>(path: string, options?: RequestInit) => Promise<T>;

// Keep a successful save even when publication fails, and always publish the
// exact saved version. Errors propagate so a failed save cannot publish.
export async function persistEditor<T extends EditablePost>(
  api: ContentAPI, post: T, blocks: unknown[], dirty: boolean, publish: boolean,
  onSaved: (post: T) => void,
): Promise<T> {
  let current = post;
  if (dirty) {
    const response = await api<{ post: T }>(`/admin/posts/${post.id}`, {
      method: "PATCH",
      body: JSON.stringify({ title: post.title, excerpt: post.excerpt ?? "", blocks, expected_version: post.edit_version }),
    });
    current = response.post;
    onSaved(current);
  }
  if (publish) {
    const response = await api<{ post: T }>(`/admin/posts/${post.id}/publish`, {
      method: "POST", body: JSON.stringify({ expected_version: current.edit_version }),
    });
    current = response.post;
  }
  return current;
}
