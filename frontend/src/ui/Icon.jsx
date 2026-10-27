import { useId } from "react";

/*
 * The app's one icon set: hand-drawn, Lucide-like strokes on a 24-unit grid,
 * rendered at 16px with a 1.5px stroke and round caps. The stroke width is
 * scaled so a line is `stroke` CSS pixels thick at any `size`.
 */
const dots = (cy = 12) => (
  <g fill="currentColor" stroke="none">
    <circle cx="5.5" cy={cy} r="1.6" />
    <circle cx="12" cy={cy} r="1.6" />
    <circle cx="18.5" cy={cy} r="1.6" />
  </g>
);
const circle = <circle cx="12" cy="12" r="9" />;
const fileShape = (
  <>
    <path d="M14 3H7a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2V8z" />
    <path d="M14 3v5h5" />
  </>
);
const folderShape = <path d="M3 7a2 2 0 0 1 2-2h4.2l2 2.5H19a2 2 0 0 1 2 2V18a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z" />;

const PATHS = {
  home: (
    <>
      <path d="M3.5 10.5 12 3.5l8.5 7" />
      <path d="M5.5 9v10.5a1 1 0 0 0 1 1H10v-6h4v6h3.5a1 1 0 0 0 1-1V9" />
    </>
  ),
  file: fileShape,
  "file-text": (
    <>
      {fileShape}
      <path d="M9 13h6M9 17h6M9 9h1.5" />
    </>
  ),
  "file-plus": (
    <>
      {fileShape}
      <path d="M12 11.5v6M9 14.5h6" />
    </>
  ),
  folder: folderShape,
  "folder-open": (
    <>
      <path d="M4.5 19.5 3 18V7a2 2 0 0 1 2-2h4.2l2 2.5H17a2 2 0 0 1 2 2V11" />
      <path d="M4.5 19.5 7 12.6a1.5 1.5 0 0 1 1.4-1H21a.8.8 0 0 1 .75 1.06l-2.3 6.3a2 2 0 0 1-1.9 1.34z" />
    </>
  ),
  "folder-plus": (
    <>
      {folderShape}
      <path d="M12 10.5v6M9 13.5h6" />
    </>
  ),
  chevron: <path d="m9.5 6 6 6-6 6" />,
  "chevron-right": <path d="m9.5 6 6 6-6 6" />,
  "chevron-left": <path d="m14.5 6-6 6 6 6" />,
  "chevron-down": <path d="m6 9.5 6 6 6-6" />,
  "chevron-up": <path d="m6 14.5 6-6 6 6" />,
  "chevrons-up-down": <path d="m7.5 15 4.5 4.5 4.5-4.5M7.5 9 12 4.5 16.5 9" />,
  message: <path d="M20.5 15a2 2 0 0 1-2 2H8l-4.5 3.5V5.5a2 2 0 0 1 2-2h13a2 2 0 0 1 2 2z" />,
  graph: (
    <>
      <circle cx="6" cy="6.5" r="2.5" />
      <circle cx="18" cy="8.5" r="2.5" />
      <circle cx="9.5" cy="18" r="2.5" />
      <path d="m8.5 7 7 1M6.9 8.9l1.7 6.7M16.4 10.5l-5.2 5.6" />
    </>
  ),
  sparkles: (
    <>
      <path d="m12 3.5 1.6 4.4a2 2 0 0 0 1.2 1.2L19.2 11l-4.4 1.6a2 2 0 0 0-1.2 1.2L12 18.2l-1.6-4.4a2 2 0 0 0-1.2-1.2L4.8 11l4.4-1.6a2 2 0 0 0 1.2-1.2z" />
      <path d="M19 3v3.5M17.25 4.75h3.5M5 17.5v3M3.5 19h3" />
    </>
  ),
  wand: (
    <>
      <path d="m3.5 20.5 10-10" />
      <path d="m12.5 9.5 2 2" />
      <path d="M16 3v2.5M16 13v2M10.5 8H8.5M21 8h-2.5M19.5 4.5l-1.2 1.2M19.5 11.5l-1.2-1.2M12.5 4.5l1.2 1.2" />
    </>
  ),
  book: <path d="M5 19.5v-14A2.5 2.5 0 0 1 7.5 3H19v14.5H7.5A2.5 2.5 0 0 0 5 20a1.5 1.5 0 0 0 1.5 1.5H19" />,
  "book-open": (
    <>
      <path d="M2.5 4.5h6a3.5 3.5 0 0 1 3.5 3.5v12.5a2.5 2.5 0 0 0-2.5-2.5h-7z" />
      <path d="M21.5 4.5h-6A3.5 3.5 0 0 0 12 8v12.5a2.5 2.5 0 0 1 2.5-2.5h7z" />
    </>
  ),
  "git-branch": (
    <>
      <path d="M6 3.5v11.5" />
      <circle cx="18" cy="6" r="2.5" />
      <circle cx="6" cy="18" r="2.5" />
      <path d="M18 8.5a9 9 0 0 1-9 9" />
    </>
  ),
  "git-commit": (
    <>
      <circle cx="12" cy="12" r="3.5" />
      <path d="M3 12h5.5M15.5 12H21" />
    </>
  ),
  refresh: (
    <>
      <path d="M20 11a8 8 0 0 0-14.3-4.6L4 8.5" />
      <path d="M4 3.5v5h5" />
      <path d="M4 13a8 8 0 0 0 14.3 4.6L20 15.5" />
      <path d="M20 20.5v-5h-5" />
    </>
  ),
  "cloud-sync": (
    <>
      <path d="M7 19a4.5 4.5 0 0 1-.7-8.95A6 6 0 0 1 17.9 9a4 4 0 0 1-.4 10" />
      <path d="M9.5 15.5a2.6 2.6 0 0 1 4.6-1.7M14.5 17.5a2.6 2.6 0 0 1-4.6 1.7" />
      <path d="M14.4 12.2v1.7h-1.7M9.6 20.8v-1.7h1.7" />
    </>
  ),
  cloud: <path d="M7 19a4.5 4.5 0 0 1-.7-8.95A6 6 0 0 1 17.9 9 4 4 0 0 1 17.5 19z" />,
  plug: (
    <>
      <path d="M9 2.5V7M15 2.5V7" />
      <path d="M6 7h12v3.5a6 6 0 0 1-12 0z" />
      <path d="M12 16.5v5" />
    </>
  ),
  terminal: (
    <>
      <path d="m5 7 5 5-5 5" />
      <path d="M12.5 17.5H19" />
    </>
  ),
  key: (
    <>
      <circle cx="7.5" cy="15.5" r="4" />
      <path d="m10.4 12.6 9.1-9.1M16.5 6.5l2.5 2.5M14 9l2 2" />
    </>
  ),
  settings: (
    <>
      <path d="M12 2.8 20 7.4v9.2l-8 4.6-8-4.6V7.4z" />
      <circle cx="12" cy="12" r="3" />
    </>
  ),
  search: (
    <>
      <circle cx="11" cy="11" r="6.5" />
      <path d="m20 20-4.2-4.2" />
    </>
  ),
  plus: <path d="M12 5v14M5 12h14" />,
  minus: <path d="M5 12h14" />,
  more: dots(),
  "more-vertical": (
    <g fill="currentColor" stroke="none">
      <circle cx="12" cy="5.5" r="1.6" />
      <circle cx="12" cy="12" r="1.6" />
      <circle cx="12" cy="18.5" r="1.6" />
    </g>
  ),
  trash: (
    <>
      <path d="M4 6.5h16M10 11v6M14 11v6" />
      <path d="m6 6.5 1 12.6a2 2 0 0 0 2 1.9h6a2 2 0 0 0 2-1.9l1-12.6" />
      <path d="M9 6.5V4.5A1.5 1.5 0 0 1 10.5 3h3A1.5 1.5 0 0 1 15 4.5v2" />
    </>
  ),
  edit: (
    <>
      <path d="M16.6 3.6a2.4 2.4 0 0 1 3.4 3.4L7.5 19.5 3 21l1.5-4.5z" />
      <path d="m14.5 5.7 3.8 3.8" />
    </>
  ),
  eye: (
    <>
      <path d="M2.5 12S6 5 12 5s9.5 7 9.5 7-3.5 7-9.5 7-9.5-7-9.5-7z" />
      <circle cx="12" cy="12" r="3" />
    </>
  ),
  "eye-off": (
    <>
      <path d="M10 5.2A9.4 9.4 0 0 1 12 5c6 0 9.5 7 9.5 7a16 16 0 0 1-2.4 3.3M6.6 6.6C3.9 8.4 2.5 12 2.5 12S6 19 12 19a9.6 9.6 0 0 0 5.3-1.6" />
      <path d="M14.1 14.1A3 3 0 0 1 9.9 9.9" />
      <path d="m3 3 18 18" />
    </>
  ),
  columns: (
    <>
      <rect x="3" y="4" width="18" height="16" rx="2.5" />
      <path d="M12 4v16" />
    </>
  ),
  sidebar: (
    <>
      <rect className="st-pane" x="3.75" y="4.75" width="5.5" height="14.5" rx="1.75" fill="currentColor" stroke="none" />
      <rect x="3" y="4" width="18" height="16" rx="2.75" />
      <path d="M9.5 4v16" />
    </>
  ),
  "panel-right": (
    <>
      <rect x="3" y="4" width="18" height="16" rx="2.75" />
      <path d="M14.5 4v16" />
    </>
  ),
  x: <path d="M17.5 6.5l-11 11M6.5 6.5l11 11" />,
  check: <path d="M19.5 6.5 9.5 16.5l-5-5" />,
  copy: (
    <>
      <rect x="8.5" y="8.5" width="12" height="12" rx="2" />
      <path d="M15.5 8.5V5.5a2 2 0 0 0-2-2h-8a2 2 0 0 0-2 2v8a2 2 0 0 0 2 2h3" />
    </>
  ),
  external: (
    <>
      <path d="M14.5 3.5h6v6" />
      <path d="m10.5 13.5 10-10" />
      <path d="M18.5 13.5v5a2 2 0 0 1-2 2h-11a2 2 0 0 1-2-2v-11a2 2 0 0 1 2-2h5" />
    </>
  ),
  link: (
    <>
      <path d="M10 13.5a4.5 4.5 0 0 0 6.8.5l2.9-2.9a4.5 4.5 0 0 0-6.4-6.4l-1.6 1.6" />
      <path d="M14 10.5a4.5 4.5 0 0 0-6.8-.5l-2.9 2.9a4.5 4.5 0 0 0 6.4 6.4l1.6-1.6" />
    </>
  ),
  history: (
    <>
      <path d="M3.5 12A8.5 8.5 0 1 0 6 6l-2.5 2.5" />
      <path d="M3.5 3.5v5h5" />
      <path d="M12 7.5V12l3.5 2" />
    </>
  ),
  list: <path d="M8.5 6h12M8.5 12h12M8.5 18h12M3.5 6h.01M3.5 12h.01M3.5 18h.01" />,
  outline: <path d="M20.5 6H8M20.5 12h-8M20.5 18h-8M3.5 6v4.5a2 2 0 0 0 2 2H8M3.5 10v6a2 2 0 0 0 2 2H8" />,
  "at-sign": (
    <>
      <circle cx="12" cy="12" r="4" />
      <path d="M16 8v5a2.75 2.75 0 0 0 5.5 0v-1a9.5 9.5 0 1 0-3.7 7.5" />
    </>
  ),
  backlink: (
    <>
      <path d="M9 14.5 4 9.5l5-5" />
      <path d="M4 9.5h10.5a5.5 5.5 0 0 1 0 11H11" />
    </>
  ),
  upload: (
    <>
      <path d="M20.5 15v3.5a2 2 0 0 1-2 2h-13a2 2 0 0 1-2-2V15" />
      <path d="m16.5 8-4.5-4.5L7.5 8M12 3.5V15" />
    </>
  ),
  download: (
    <>
      <path d="M20.5 15v3.5a2 2 0 0 1-2 2h-13a2 2 0 0 1-2-2V15" />
      <path d="m7.5 10.5 4.5 4.5 4.5-4.5M12 15V3.5" />
    </>
  ),
  magpie: (
    <>
      <path d="M21.5 7.4 18.4 8a3.1 3.1 0 0 0-5.9.6c-.3 1.3-1.2 2.3-2.6 2.9C6.6 13 4.6 15.9 2.5 21c3.1-1.4 5.6-2.5 8-2.9 4.8-.7 7.6-3.9 7.9-8.6z" />
      <path d="m10.5 14.2 3.6 2.6" />
      <circle cx="15.5" cy="8.4" r=".9" fill="currentColor" stroke="none" />
    </>
  ),
  sun: (
    <>
      <circle cx="12" cy="12" r="4" />
      <path d="M12 2.5v2M12 19.5v2M5.3 5.3l1.4 1.4M17.3 17.3l1.4 1.4M2.5 12h2M19.5 12h2M5.3 18.7l1.4-1.4M17.3 6.7l1.4-1.4" />
    </>
  ),
  moon: <path d="M20.5 14.2A8.5 8.5 0 1 1 9.8 3.5a6.6 6.6 0 0 0 10.7 10.7z" />,
  monitor: (
    <>
      <rect x="2.5" y="3.5" width="19" height="13" rx="2" />
      <path d="M8 20.5h8M12 16.5v4" />
    </>
  ),
  alert: (
    <>
      <path d="M10.3 4 2.2 18a2 2 0 0 0 1.7 3h16.2a2 2 0 0 0 1.7-3L13.7 4a2 2 0 0 0-3.4 0z" />
      <path d="M12 9.5v4M12 17h.01" />
    </>
  ),
  "alert-circle": (
    <>
      {circle}
      <path d="M12 7.5v5M12 16.2h.01" />
    </>
  ),
  info: (
    <>
      {circle}
      <path d="M12 16.5v-5M12 7.8h.01" />
    </>
  ),
  "check-circle": (
    <>
      {circle}
      <path d="m8 12.2 2.8 2.8L16 9.6" />
    </>
  ),
  dot: <circle cx="12" cy="12" r="4" fill="currentColor" stroke="none" />,
  play: <path d="M7 4.8v14.4a.8.8 0 0 0 1.2.7l11.5-7.2a.8.8 0 0 0 0-1.4L8.2 4.1a.8.8 0 0 0-1.2.7z" />,
  present: (
    <>
      <path d="M2.5 3.5h19M20.5 3.5v10.5a2 2 0 0 1-2 2h-13a2 2 0 0 1-2-2V3.5" />
      <path d="m7.5 21 4.5-5 4.5 5" />
    </>
  ),
  inbox: (
    <>
      <path d="M21.5 12.5h-6l-2 3h-3l-2-3h-6" />
      <path d="M5.5 5.1 2.5 12.5v5a2 2 0 0 0 2 2h15a2 2 0 0 0 2-2v-5l-3-7.4a2 2 0 0 0-1.8-1.1H7.3a2 2 0 0 0-1.8 1.1z" />
    </>
  ),
  prompt: (
    <>
      <path d="M8 20.5h11a2 2 0 0 0 2-2v-2H10v2a2 2 0 1 1-4 0v-13a2 2 0 1 0-4 0v2.5h4" />
      <path d="M18.5 16.5v-11a2 2 0 0 0-2-2H4" />
      <path d="M14.5 8H10M14.5 12H10" />
    </>
  ),
  logout: (
    <>
      <path d="M9 20.5H5.5a2 2 0 0 1-2-2v-13a2 2 0 0 1 2-2H9" />
      <path d="m15.5 16.5 4.5-4.5-4.5-4.5M20 12H9" />
    </>
  ),
  lock: (
    <>
      <rect x="4.5" y="10.5" width="15" height="10" rx="2" />
      <path d="M8 10.5V7a4 4 0 0 1 8 0v3.5" />
    </>
  ),
  image: (
    <>
      <rect x="3.5" y="3.5" width="17" height="17" rx="2.5" />
      <circle cx="9" cy="9" r="1.75" />
      <path d="m20.5 15-3.4-3.4a2 2 0 0 0-2.8 0L5.5 20.5" />
    </>
  ),
  code: <path d="m15.5 17.5 5.5-5.5-5.5-5.5M8.5 6.5 3 12l5.5 5.5" />,
  braces: (
    <>
      <path d="M8 3.5H7a2 2 0 0 0-2 2v4a2 2 0 0 1-2 2 2 2 0 0 1 2 2v5a2 2 0 0 0 2 2h1" />
      <path d="M16 3.5h1a2 2 0 0 1 2 2v4a2 2 0 0 0 2 2 2 2 0 0 0-2 2v5a2 2 0 0 1-2 2h-1" />
    </>
  ),
  globe: (
    <>
      {circle}
      <path d="M3 12h18M12 3a14 14 0 0 1 3.6 9A14 14 0 0 1 12 21a14 14 0 0 1-3.6-9A14 14 0 0 1 12 3z" />
    </>
  ),
  server: (
    <>
      <rect x="3" y="3.5" width="18" height="7" rx="2" />
      <rect x="3" y="13.5" width="18" height="7" rx="2" />
      <path d="M7 7h.01M7 17h.01" />
    </>
  ),
  zap: <path d="M13 2.5 3.5 14H12l-1 7.5L20.5 10H12z" />,
  cpu: (
    <>
      <rect x="6" y="6" width="12" height="12" rx="2" />
      <path d="M9.5 2.5V6M14.5 2.5V6M9.5 18v3.5M14.5 18v3.5M2.5 9.5H6M2.5 14.5H6M18 9.5h3.5M18 14.5h3.5" />
    </>
  ),
  tag: (
    <>
      <path d="M12.6 3.1A2 2 0 0 0 11.2 2.5H4.5a2 2 0 0 0-2 2v6.7a2 2 0 0 0 .6 1.4l8.6 8.6a2.3 2.3 0 0 0 3.2 0l6.2-6.2a2.3 2.3 0 0 0 0-3.2z" />
      <circle cx="7.5" cy="7.5" r="1.1" fill="currentColor" stroke="none" />
    </>
  ),
  "arrow-left": <path d="M19 12H5M11.5 5.5 5 12l6.5 6.5" />,
  "arrow-right": <path d="M5 12h14M12.5 5.5 19 12l-6.5 6.5" />,
  "arrow-up": <path d="M12 19V5M5.5 11.5 12 5l6.5 6.5" />,
  "arrow-down": <path d="M12 5v14M5.5 12.5 12 19l6.5-6.5" />,
  "arrow-up-right": <path d="M7 17 17 7M8 7h9v9" />,
  pin: (
    <>
      <path d="M12 17v4.5" />
      <path d="M9 10.8V4h6v6.8l2.5 3.2v1.5h-11V14z" />
    </>
  ),
  star: <path d="m12 3 2.8 5.7 6.2.9-4.5 4.4 1.1 6.2L12 17.3l-5.6 2.9 1.1-6.2L3 9.6l6.2-.9z" />,
  filter: <path d="M3.5 4.5h17l-6.5 7.7v6.3l-4 2v-8.3z" />,
  layers: (
    <>
      <path d="m12 3 9 4.5-9 4.5-9-4.5z" />
      <path d="m3 12 9 4.5 9-4.5M3 16.5 12 21l9-4.5" />
    </>
  ),
  user: (
    <>
      <circle cx="12" cy="8" r="4" />
      <path d="M4.5 20.5a7.5 7.5 0 0 1 15 0" />
    </>
  ),
  bolt: <path d="M13 2.5 3.5 14H12l-1 7.5L20.5 10H12z" />,
  move: (
    <>
      <path d="M3.5 12h11M11 8.5l3.5 3.5-3.5 3.5" />
      <path d="M14 4.5h4.5a2 2 0 0 1 2 2v11a2 2 0 0 1-2 2H14" />
    </>
  ),
  type: <path d="M5 7V5h14v2M12 5v14M9 19h6" />,
  keyboard: (
    <>
      <rect x="2.5" y="5.5" width="19" height="13" rx="2" />
      <path d="M6.5 9.5h.01M10 9.5h.01M14 9.5h.01M17.5 9.5h.01M7 14.5h10" />
    </>
  ),
  activity: <path d="M21.5 12h-3.8l-2.9 8.5L9.2 3.5 6.3 12H2.5" />,
};

/** Names of every icon in the set (for docs and pickers). */
export const ICON_NAMES = Object.keys(PATHS);

/**
 * An inline SVG icon.
 *
 * @param {object} props
 * @param {string} props.name     one of ICON_NAMES
 * @param {number} [props.size]   rendered size in CSS px (default 16)
 * @param {number} [props.stroke] stroke width in CSS px at that size (default 1.5)
 * @param {string} [props.title]  accessible label; the icon is aria-hidden without one
 */
export default function Icon({ name, size = 16, stroke = 1.5, className = "", title, style, ...rest }) {
  const shape = PATHS[name] ?? PATHS.dot;
  return (
    <svg
      width={size}
      height={size}
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth={(stroke * 24) / size}
      strokeLinecap="round"
      strokeLinejoin="round"
      className={`ic-svg ${className}`.trim()}
      style={style}
      aria-hidden={title ? undefined : true}
      role={title ? "img" : undefined}
      focusable="false"
      {...rest}
    >
      {title ? <title>{title}</title> : null}
      {shape}
    </svg>
  );
}

/**
 * The Marginalia mark: an indigo rounded square with a folded "M" — the app
 * icon in miniature (same drawing as resources/icon.png).
 */
export function Logo({ size = 18, className = "" }) {
  const id = useId().replace(/:/g, "");
  return (
    <svg width={size} height={size} viewBox="0 0 32 32" className={`logo ${className}`.trim()} aria-hidden="true" focusable="false">
      <defs>
        <linearGradient id={`lg${id}`} x1="0" y1="0" x2="1" y2="1">
          <stop offset="0" stopColor="#c8442b" />
          <stop offset="1" stopColor="#e2673f" />
        </linearGradient>
      </defs>
      <rect width="32" height="32" rx="7.2" fill={`url(#lg${id})`} />
      <path d="M9 22.5V10l7 7.2L23 10v12.5" fill="none" stroke="#fff" strokeWidth="2.6" strokeLinecap="round" strokeLinejoin="round" />
    </svg>
  );
}
