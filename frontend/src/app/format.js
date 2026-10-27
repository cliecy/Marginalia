/**
 * Small formatting helpers shared by pages (Chinese UI copy).
 */

const toDate = (value) => {
  if (value instanceof Date) return value;
  if (typeof value === "number") return new Date(value < 1e12 ? value * 1000 : value); // seconds or ms
  return new Date(value);
};

/**
 * "刚刚" · "5 分钟前" · "3 小时前" · "昨天 14:20" · "3 天前" · "9月12日" · "2024年9月12日".
 *
 * @param {Date|number|string} value a Date, epoch seconds/ms, or an ISO string
 */
export function timeAgo(value, now = Date.now()) {
  const date = toDate(value);
  const t = date.getTime();
  if (!Number.isFinite(t)) return "";
  const diff = Math.max(0, now - t) / 1000;
  if (diff < 45) return "刚刚";
  if (diff < 3600) return `${Math.round(diff / 60)} 分钟前`;
  const today = new Date(now);
  const sameDay = date.toDateString() === today.toDateString();
  if (sameDay || diff < 6 * 3600) return `${Math.round(diff / 3600)} 小时前`;
  const yesterday = new Date(now - 86400000);
  const hm = `${String(date.getHours()).padStart(2, "0")}:${String(date.getMinutes()).padStart(2, "0")}`;
  if (date.toDateString() === yesterday.toDateString()) return `昨天 ${hm}`;
  if (diff < 7 * 86400) return `${Math.ceil(diff / 86400)} 天前`;
  if (date.getFullYear() === today.getFullYear()) return `${date.getMonth() + 1}月${date.getDate()}日`;
  return `${date.getFullYear()}年${date.getMonth() + 1}月${date.getDate()}日`;
}

/** "2024-09-12 14:20" in local time. */
export function formatDateTime(value) {
  const date = toDate(value);
  if (!Number.isFinite(date.getTime())) return "";
  const pad = (n) => String(n).padStart(2, "0");
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())} ${pad(date.getHours())}:${pad(date.getMinutes())}`;
}

/** A count with thin grouping: 12345 → "12,345". */
export const formatCount = (n) => (Number.isFinite(n) ? n.toLocaleString("en-US") : "—");

/** 1536 → "1.5 KB". */
export function formatBytes(bytes) {
  if (!Number.isFinite(bytes)) return "";
  const units = ["B", "KB", "MB", "GB"];
  let value = bytes;
  let unit = 0;
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024;
    unit += 1;
  }
  return `${unit ? value.toFixed(value < 10 ? 1 : 0) : value} ${units[unit]}`;
}

/** "Knowledge/机器学习/PPO.md" → "PPO". */
export const noteTitle = (path) => (path || "").split("/").pop().replace(/\.md$/i, "");

/** "Knowledge/机器学习/PPO.md" → "Knowledge/机器学习". */
export const parentFolder = (path) => (path || "").split("/").slice(0, -1).join("/");
