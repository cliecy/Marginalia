/**
 * Options for a model picker: grouped by provider prefix ("group/…",
 * "codex/…"), groups in the order the server lists them, each option showing
 * Magpie's display name with the model id beside it.
 *
 * @param {string[]} models  model ids from /api/ai/models
 * @param {Record<string, string>} labels  display names by id
 * @param {string} [current] an id to include even when the server no longer lists it
 * @returns {Array<{value: string, label: string, hint?: string, group?: string}>}
 */
export function modelOptions(models, labels = {}, current = "") {
  const ids = [...new Set(current && !models.includes(current) ? [current, ...models] : models)];
  const groupOf = (id) => (id.includes("/") ? id.split("/")[0] : "");
  const order = new Map();
  for (const id of ids) if (!order.has(groupOf(id))) order.set(groupOf(id), order.size);
  return ids
    .map((id, index) => ({ id, index, group: groupOf(id) }))
    .sort((a, b) => order.get(a.group) - order.get(b.group) || a.index - b.index)
    .map(({ id, group }) => {
      const label = labels[id] && labels[id] !== id ? labels[id] : null;
      return { value: id, label: label ?? id, hint: label ? id : undefined, group: group || undefined, keywords: id };
    });
}

/** "codex/gpt-6.1-sol" → its display name, or the id itself. */
export const modelLabel = (id, labels = {}) => (id && labels[id]) || id || "";
