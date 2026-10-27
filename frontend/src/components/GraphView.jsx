import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { forceCollide, forceLink, forceManyBody, forceSimulation, forceX, forceY } from "d3-force";
import { api } from "../lib/api.js";
import { useStoredState } from "../app/hooks.js";
import Button, { IconButton } from "../ui/Button.jsx";
import EmptyState from "../ui/EmptyState.jsx";
import { SearchInput } from "../ui/Field.jsx";
import Spinner from "../ui/Spinner.jsx";
import Toggle from "../ui/Toggle.jsx";

const MIN_ZOOM = 0.15;
const MAX_ZOOM = 4;
const DRAG_THRESHOLD = 3;
const LEGEND_MAX = 10;
const ROOT_GROUP = "根目录";
const CHAT_GROUP = "对话";
/** Chats keep one quiet colour, so toggling them never recolours the domains. */
const CHAT_COLOUR = "var(--muted)";
const CHAT_STAMP = /^\d{4}-\d{2}-\d{2} \d{4} /;

/** Colour group of a note: Knowledge/<domain>/… by domain, otherwise its top-level folder. */
const groupOf = (id) => {
  const parts = id.split("/");
  if (parts.length === 1) return ROOT_GROUP;
  return parts[0] === "Knowledge" && parts.length > 2 ? parts[1] : parts[0];
};

/** The categorical chart colours (tokens, so dark mode follows); past eight they repeat a shade deeper. */
const colourAt = (index) => {
  const base = `var(--c${(index % 8) + 1})`;
  return index < 8 ? base : `color-mix(in srgb, ${base} 62%, var(--fg))`;
};

const clamp = (value, lo, hi) => Math.min(hi, Math.max(lo, value));
const shorten = (text, max = 18) => (text.length > max ? `${text.slice(0, max - 1)}…` : text);

/** Rough width of a label in ems: CJK and other full-width glyphs are square, the rest about 0.58em. */
const emWidth = (text) => {
  let width = 0;
  for (const ch of text) width += ch.codePointAt(0) > 0x2e7f ? 1 : 0.58;
  return width;
};

const LABEL_CELL = 96;

/** The grid cells a box touches. */
function cellsOf(box) {
  const cells = [];
  for (let gx = Math.floor(box.x0 / LABEL_CELL); gx <= Math.floor(box.x1 / LABEL_CELL); gx += 1) {
    for (let gy = Math.floor(box.y0 / LABEL_CELL); gy <= Math.floor(box.y1 / LABEL_CELL); gy += 1) cells.push(`${gx},${gy}`);
  }
  return cells;
}

/**
 * Where a node's label can go, in order of preference: under the dot, over it,
 * or to its right. Each with the text position and the box it covers.
 */
function labelSpots(node, width, fontSize, pad) {
  const half = width / 2 + pad;
  const box = (x0, x1, baseline) => ({ x0, x1, y0: baseline - fontSize - pad, y1: baseline + fontSize * 0.3 + pad });
  const below = node.y + node.r + 3 + fontSize;
  const above = node.y - node.r - 3 - fontSize * 0.3;
  const side = node.y + fontSize * 0.35;
  const left = node.x + node.r + 4;
  return [
    { x: node.x, y: below, anchor: "middle", box: box(node.x - half, node.x + half, below) },
    { x: node.x, y: above, anchor: "middle", box: box(node.x - half, node.x + half, above) },
    { x: left, y: side, anchor: "start", box: box(left - pad, left + width + pad, side) },
  ];
}

/**
 * Chooses which node labels to draw, and where, so that none covers another
 * label or another note's dot: candidates are taken in priority order, each
 * tries under / over / beside its dot, and is kept at the first spot that
 * misses every dot and every label kept before it (a coarse grid keeps the
 * test cheap). Forced labels (the hovered note and the current one) are always
 * drawn — at a free spot when there is one, else under the dot.
 *
 * Boxes are in world units (the labels scale with the graph), so the result
 * only changes when nodes move or the font size does.
 *
 * @param {Array<{node: object, text: string, forced: boolean}>} candidates sorted by priority
 * @param {Array<{id: string, x: number, y: number, r: number}>} nodes every node (their dots are obstacles)
 * @param {number} fontSize label font size in world units
 * @param {{x0: number, y0: number, x1: number, y1: number}} bounds visible world rectangle
 * @returns {Map<string, {x: number, y: number, anchor: "middle"|"start"}>} where each drawn label goes
 */
function placeLabels(candidates, nodes, fontSize, bounds) {
  const grid = new Map();
  const add = (box) => {
    for (const key of cellsOf(box)) {
      if (!grid.has(key)) grid.set(key, []);
      grid.get(key).push(box);
    }
  };
  for (const n of nodes) add({ x0: n.x - n.r - 1, x1: n.x + n.r + 1, y0: n.y - n.r - 1, y1: n.y + n.r + 1, id: n.id });
  const placed = new Map();
  const pad = fontSize * 0.25;
  const visible = (box) => !(box.x1 < bounds.x0 || box.x0 > bounds.x1 || box.y1 < bounds.y0 || box.y0 > bounds.y1);
  // A label may touch its own dot (it hangs right under it), nothing else.
  const free = (box, id) =>
    !cellsOf(box).some((key) => grid.get(key)?.some((other) => other.id !== id && box.x0 < other.x1 && box.x1 > other.x0 && box.y0 < other.y1 && box.y1 > other.y0));
  for (const { node, text, forced } of candidates) {
    const spots = labelSpots(node, emWidth(text) * fontSize, fontSize, pad);
    let spot = spots.find((s) => (forced || visible(s.box)) && free(s.box, node.id));
    if (!spot && forced) spot = spots[0];
    if (!spot) continue;
    placed.set(node.id, { x: spot.x, y: spot.y, anchor: spot.anchor });
    add({ ...spot.box, id: node.id });
  }
  return placed;
}

/**
 * Builds the force simulation for a graph and warms it up off-screen, so the
 * first frame already shows a settled layout (the rest settles live).
 */
function buildSimulation(data) {
  const nodes = data.nodes.map((n) => ({ ...n }));
  const ids = new Set(nodes.map((n) => n.id));
  const links = data.edges.filter((e) => ids.has(e.source) && ids.has(e.target) && e.source !== e.target).map((e) => ({ ...e }));
  const degree = new Map();
  const neighbours = new Map(nodes.map((n) => [n.id, new Set()]));
  for (const e of links) {
    degree.set(e.source, (degree.get(e.source) || 0) + 1);
    degree.set(e.target, (degree.get(e.target) || 0) + 1);
    neighbours.get(e.source).add(e.target);
    neighbours.get(e.target).add(e.source);
  }
  for (const n of nodes) {
    n.degree = degree.get(n.id) || 0;
    n.r = 4.5 + Math.min(13, Math.sqrt(n.degree) * 2.8);
    n.chat = n.type === "chat" || n.id.startsWith("Chats/");
    n.group = n.chat ? CHAT_GROUP : groupOf(n.id);
    // A note without a title is named after its file; a chat's file name starts with a stamp ("2026-10-26 1030 slug").
    const title = String(n.title || n.id.split("/").pop().replace(/\.md$/i, ""));
    n.title = title;
    n.label = shorten(n.chat ? title.replace(CHAT_STAMP, "") || title : title);
  }
  const simulation = forceSimulation(nodes)
    .force("link", forceLink(links).id((d) => d.id).distance(nodes.length < 50 ? 96 : 84).strength(0.6))
    .force("charge", forceManyBody().strength(-210).distanceMax(640))
    // Linked notes are held by their links; lone ones need a firmer pull or they drift to the edges.
    .force("x", forceX(0).strength((d) => (d.degree ? 0.045 : 0.16)))
    .force("y", forceY(0).strength((d) => (d.degree ? 0.045 : 0.16)))
    // Room for the label hanging under each dot, not just the dot.
    .force("collide", forceCollide().radius((d) => Math.max(d.r + 6, emWidth(d.label) * 11 * 0.45)).strength(0.9))
    .stop();
  const warm = nodes.length > 800 ? 40 : nodes.length > 300 ? 120 : 260;
  for (let i = 0; i < warm; i += 1) simulation.tick();
  return { simulation, nodes, links, neighbours };
}

/** The view that shows every node (or the given ones) inside a w×h box. */
function fitView(nodes, w, h) {
  if (!nodes.length || !w || !h) return { x: 0, y: 0, k: 1 };
  let x0 = Infinity;
  let y0 = Infinity;
  let x1 = -Infinity;
  let y1 = -Infinity;
  for (const n of nodes) {
    // Leave room for the label under each dot (about 11px type at 100%).
    const half = Math.max(n.r, (emWidth(n.label || "") * 11) / 2);
    x0 = Math.min(x0, n.x - half);
    y0 = Math.min(y0, n.y - n.r);
    x1 = Math.max(x1, n.x + half);
    y1 = Math.max(y1, n.y + n.r + 18);
  }
  const padX = 48;
  const padTop = 64; // the floating toolbar
  const padBottom = 56; // the legend
  const k = clamp(Math.min((w - padX * 2) / Math.max(x1 - x0, 1), (h - padTop - padBottom) / Math.max(y1 - y0, 1)), 0.25, 1.35);
  const cx = (x0 + x1) / 2;
  const cy = (y0 + y1) / 2;
  return { x: -cx * k, y: -cy * k + (padTop - padBottom) / 2, k };
}

/**
 * The knowledge graph: notes as nodes (coloured by domain, sized by links),
 * [[links]] as edges, laid out by d3-force. A floating pill toolbar searches
 * and highlights nodes, shows or hides chats, and zooms; the legend under it
 * picks out a domain. Drag the canvas to pan, scroll or pinch to zoom, drag a
 * node to move it, click it to open the note.
 *
 * @param {object} props
 * @param {string|null} [props.current]  the note to mark (the last one opened)
 * @param {(path: string) => void} props.onOpen
 * @param {number} [props.reloadKey]     change it to fetch the graph again
 */
export default function GraphView({ current = null, onOpen, reloadKey = 0 }) {
  const [state, setState] = useState({ data: null, error: null, loading: true });
  const [showChats, setShowChats] = useStoredState("mn.graph.chats", false);
  const [, setFrame] = useState(0);
  const [view, setView] = useState({ x: 0, y: 0, k: 1 });
  const [gliding, setGliding] = useState(false);
  const [size, setSize] = useState({ w: 0, h: 0 });
  const [hover, setHover] = useState(null);
  const [query, setQuery] = useState("");
  const [cursor, setCursor] = useState(0);
  const [focusGroup, setFocusGroup] = useState(null);
  const [dragging, setDragging] = useState(false);
  const box = useRef(null);
  const svg = useRef(null);
  const drag = useRef(null);
  const glideTimer = useRef(0);
  const viewRef = useRef(view);
  viewRef.current = view;
  const sizeRef = useRef(size);
  sizeRef.current = size;
  // The view follows the layout (fits it as it settles, and on resize) until
  // the reader pans, zooms or drags; "适应窗口" hands it back.
  const autoFit = useRef(true);
  useEffect(() => () => clearTimeout(glideTimer.current), []);

  // ------------------------------------------------------------ data
  const load = useCallback(() => {
    setState((s) => ({ ...s, loading: true, error: null }));
    api
      .graph(showChats)
      .then((data) => setState({ data: { nodes: data?.nodes || [], edges: data?.edges || [] }, error: null, loading: false }))
      .catch((error) => setState((s) => ({ ...s, error, loading: false })));
  }, [showChats]);
  useEffect(load, [load, reloadKey]);

  const sim = useMemo(() => (state.data ? buildSimulation(state.data) : null), [state.data]);

  /** Moves the view smoothly (buttons, search, fit) rather than jumping. */
  const glideTo = useCallback((next) => {
    setGliding(true);
    setView(next);
    clearTimeout(glideTimer.current);
    glideTimer.current = setTimeout(() => setGliding(false), 420);
  }, []);

  // Domains are coloured by size (largest first); chats come last in a colour of their own.
  const groups = useMemo(() => {
    const counts = new Map();
    for (const n of sim?.nodes || []) counts.set(n.group, (counts.get(n.group) || 0) + 1);
    const chats = counts.get(CHAT_GROUP);
    counts.delete(CHAT_GROUP);
    const ordered = [...counts].sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0], "zh-Hans-CN"));
    const result = new Map(ordered.map(([name, count], index) => [name, { colour: colourAt(index), count }]));
    if (chats) result.set(CHAT_GROUP, { colour: CHAT_COLOUR, count: chats });
    return result;
  }, [sim]);

  // ------------------------------------------------------------ size & first fit
  useEffect(() => {
    const node = box.current;
    if (!node) return undefined;
    // Measure immediately: WebKit can defer ResizeObserver while a window is
    // backgrounded, leaving the graph centred at (0, 0) until the next paint.
    const rect = node.getBoundingClientRect();
    setSize({ w: rect.width, h: rect.height });
    const observer = new ResizeObserver(([entry]) => setSize({ w: entry.contentRect.width, h: entry.contentRect.height }));
    observer.observe(node);
    return () => observer.disconnect();
  }, []);

  const fitted = useRef(null);
  useEffect(() => {
    if (!sim || !size.w) return;
    if (fitted.current !== sim) {
      fitted.current = sim;
      autoFit.current = true;
    }
    if (autoFit.current) setView(fitView(sim.nodes, size.w, size.h));
  }, [sim, size.w, size.h]);

  // ------------------------------------------------------------ live layout (one render per frame)
  useEffect(() => {
    if (!sim) return undefined;
    let raf = 0;
    sim.simulation.on("tick", () => {
      if (!raf) raf = requestAnimationFrame(() => ((raf = 0), setFrame((f) => f + 1)));
    });
    // Once the layout has settled, frame it again (it spreads out a little as it cools).
    sim.simulation.on("end", () => {
      if (autoFit.current) glideTo(fitView(sim.nodes, sizeRef.current.w, sizeRef.current.h));
    });
    sim.simulation.restart();
    return () => {
      sim.simulation.on("tick", null).on("end", null).stop();
      if (raf) cancelAnimationFrame(raf);
    };
  }, [sim, glideTo]);

  const zoomBy = (factor) => {
    autoFit.current = false;
    const v = viewRef.current;
    const k = clamp(v.k * factor, MIN_ZOOM, MAX_ZOOM);
    glideTo({ x: (v.x * k) / v.k, y: (v.y * k) / v.k, k });
  };
  const fit = () => {
    if (!sim) return;
    autoFit.current = true;
    glideTo(fitView(sim.nodes, size.w, size.h));
  };
  const centreOn = (node, minZoom = 1.1) => {
    autoFit.current = false;
    const k = Math.max(viewRef.current.k, minZoom);
    glideTo({ x: -node.x * k, y: -node.y * k, k });
  };

  // Wheel / pinch zoom toward the pointer (a native listener: React's is passive).
  useEffect(() => {
    const node = svg.current;
    if (!node) return undefined;
    const onWheel = (event) => {
      event.preventDefault();
      autoFit.current = false;
      setGliding(false);
      const rect = node.getBoundingClientRect();
      const px = event.clientX - rect.left - rect.width / 2;
      const py = event.clientY - rect.top - rect.height / 2;
      const speed = event.ctrlKey ? 0.01 : event.deltaMode === 1 ? 0.05 : 0.0022;
      setView((v) => {
        const k = clamp(v.k * Math.exp(-event.deltaY * speed), MIN_ZOOM, MAX_ZOOM);
        const wx = (px - v.x) / v.k;
        const wy = (py - v.y) / v.k;
        return { x: px - wx * k, y: py - wy * k, k };
      });
    };
    node.addEventListener("wheel", onWheel, { passive: false });
    return () => node.removeEventListener("wheel", onWheel);
  }, [sim]);

  // ------------------------------------------------------------ pointer
  const toWorld = (event) => {
    const rect = svg.current.getBoundingClientRect();
    const v = viewRef.current;
    return { x: (event.clientX - rect.left - rect.width / 2 - v.x) / v.k, y: (event.clientY - rect.top - rect.height / 2 - v.y) / v.k };
  };

  const onPointerDown = (event, node) => {
    if (event.button !== 0) return;
    event.stopPropagation();
    svg.current.setPointerCapture(event.pointerId);
    setGliding(false);
    drag.current = { node, startX: event.clientX, startY: event.clientY, origin: { ...viewRef.current }, moved: false };
    setDragging(true);
  };

  const onPointerMove = (event) => {
    const d = drag.current;
    if (!d) return;
    if (!d.moved && Math.hypot(event.clientX - d.startX, event.clientY - d.startY) < DRAG_THRESHOLD) return;
    if (!d.moved) {
      d.moved = true;
      autoFit.current = false;
      if (d.node) sim.simulation.alphaTarget(0.3).restart();
    }
    if (d.node) {
      const p = toWorld(event);
      d.node.fx = p.x;
      d.node.fy = p.y;
    } else {
      setView({ ...d.origin, x: d.origin.x + event.clientX - d.startX, y: d.origin.y + event.clientY - d.startY });
    }
  };

  const onPointerUp = () => {
    const d = drag.current;
    drag.current = null;
    setDragging(false);
    if (!d?.node) return;
    if (d.moved) {
      sim.simulation.alphaTarget(0);
      d.node.fx = null;
      d.node.fy = null;
    } else {
      onOpen?.(d.node.id);
    }
  };

  // ------------------------------------------------------------ search
  const q = query.trim().toLowerCase();
  const matches = useMemo(() => {
    if (!q || !sim) return [];
    return sim.nodes.filter((n) => String(n.title || "").toLowerCase().includes(q) || n.id.toLowerCase().includes(q)).sort((a, b) => b.degree - a.degree);
  }, [q, sim]);
  const matchIds = useMemo(() => new Set(matches.map((n) => n.id)), [matches]);
  useEffect(() => setCursor(0), [q]);

  const onSearchEnter = () => {
    if (!matches.length) return;
    const node = matches[cursor % matches.length];
    centreOn(node, 1.2);
    setCursor((c) => c + 1);
  };

  const onKeyDown = (event) => {
    if (event.target.closest?.("input, textarea, [contenteditable]")) return;
    if (event.key === "Escape") {
      setQuery("");
      setFocusGroup(null);
    } else if (event.key === "+" || event.key === "=") zoomBy(1.25);
    else if (event.key === "-") zoomBy(0.8);
    else if (event.key === "0") fit();
  };

  // ------------------------------------------------------------ states
  const frame = (content) => (
    <div className="graph" ref={box}>
      {content}
    </div>
  );
  if (!sim) {
    if (state.error)
      return frame(
        <EmptyState icon="alert-circle" title="知识图谱加载失败" actions={<Button icon="refresh" onClick={load}>重试</Button>}>
          {state.error.message}
        </EmptyState>,
      );
    return frame(
      <div className="graph-loading">
        <Spinner size={16} />
        <span>正在绘制知识图谱…</span>
      </div>,
    );
  }

  const currentNode = current ? sim.nodes.find((n) => n.id === current) : null;
  const hoverNode = hover ? sim.nodes.find((n) => n.id === hover) : null;
  const near = hoverNode ? sim.neighbours.get(hoverNode.id) : null;

  /** Is a node picked out by what the reader is doing (hover > search > domain)? */
  const lit = (n) => (hoverNode ? n.id === hoverNode.id || near.has(n.id) : q ? matchIds.has(n.id) : focusGroup ? n.group === focusGroup : true);
  const highlighting = !!hoverNode || !!q || !!focusGroup;
  const showAllLabels = view.k >= 0.85;

  const cx = size.w / 2;
  const cy = size.h / 2;

  // Labels: wanted ones (all when zoomed in, else the hubs and search hits);
  // the hovered and current notes always, then search hits, lit notes before
  // dimmed ones, then by links. A label that would overlap one already placed
  // is left out.
  const fontSize = clamp(11 / Math.sqrt(view.k), 9, 16);
  const labelCandidates = [];
  for (const n of sim.nodes) {
    const isHover = hoverNode?.id === n.id;
    const forced = isHover || n.id === current;
    const match = !!q && matchIds.has(n.id);
    const on = lit(n);
    if (!forced && !match && !showAllLabels && n.r < 10 && !(hoverNode && on)) continue;
    labelCandidates.push({ node: n, text: n.label, forced, rank: (isHover ? 4 : forced ? 3 : match ? 2 : 0) + (on ? 1 : 0) + n.degree / 1000 });
  }
  labelCandidates.sort((a, b) => b.rank - a.rank);
  const labelled = placeLabels(labelCandidates, sim.nodes, fontSize, {
    x0: (-cx - view.x) / view.k,
    x1: (cx - view.x) / view.k,
    y0: (-cy - view.y) / view.k,
    y1: (cy - view.y) / view.k,
  });
  const grid = clamp(22 * view.k, 10, 60);
  let tooltip = null;
  if (hoverNode && !dragging) {
    const tags = (hoverNode.tags || []).slice(0, 4);
    tooltip = (
      <div className="graph-tip" style={{ left: cx + view.x + hoverNode.x * view.k, top: cy + view.y + (hoverNode.y - hoverNode.r) * view.k - 10 }}>
        <b>{hoverNode.title}</b>
        <span className="graph-tip-path">{hoverNode.id}</span>
        <span className="graph-tip-meta">
          {hoverNode.degree ? `${hoverNode.degree} 条链接` : "还没有链接"}
          {tags.map((tag) => (
            <em key={tag}>#{tag}</em>
          ))}
        </span>
      </div>
    );
  }

  const domains = [...groups].filter(([name]) => name !== CHAT_GROUP);
  const legend = [...domains.slice(0, LEGEND_MAX), ...(groups.has(CHAT_GROUP) ? [[CHAT_GROUP, groups.get(CHAT_GROUP)]] : [])];
  const hiddenDomains = Math.max(0, domains.length - LEGEND_MAX);
  const empty = sim.nodes.length === 0;

  return (
    <div className="graph" ref={box} tabIndex={-1} onKeyDown={onKeyDown}>
      <svg
        ref={svg}
        className={`graph-canvas ${dragging ? "dragging" : ""}`.trim()}
        style={{ backgroundSize: `${grid}px ${grid}px`, backgroundPosition: `${cx + view.x}px ${cy + view.y}px` }}
        onPointerDown={(e) => onPointerDown(e, null)}
        onPointerMove={onPointerMove}
        onPointerUp={onPointerUp}
        onPointerCancel={onPointerUp}
        onDoubleClick={(e) => e.target === svg.current && fit()}
      >
        <g transform={`translate(${cx},${cy})`}>
          <g className={`graph-world ${gliding ? "gliding" : ""}`.trim()} style={{ transform: `translate(${view.x}px, ${view.y}px) scale(${view.k})` }}>
            <g className="graph-links">
              {sim.links.map((l, i) => {
                const hot = hoverNode && (l.source.id === hoverNode.id || l.target.id === hoverNode.id);
                const dim = highlighting && !hot && !(lit(l.source) && lit(l.target));
                return <line key={i} x1={l.source.x} y1={l.source.y} x2={l.target.x} y2={l.target.y} className={`graph-link ${hot ? "hot" : ""} ${dim ? "dim" : ""}`.trim()} />;
              })}
            </g>
            {sim.nodes.map((n) => {
              const isCurrent = n.id === current;
              const match = !!q && matchIds.has(n.id);
              const cls = ["graph-node", n.chat && "chat", highlighting && !lit(n) && "dim", isCurrent && "current", match && "match", hoverNode?.id === n.id && "hover"]
                .filter(Boolean)
                .join(" ");
              return (
                <g
                  key={n.id}
                  transform={`translate(${n.x},${n.y})`}
                  className={cls}
                  style={{ "--node": groups.get(n.group)?.colour }}
                  onPointerDown={(e) => onPointerDown(e, n)}
                  onPointerEnter={() => !drag.current && setHover(n.id)}
                  onPointerLeave={() => setHover((h) => (h === n.id ? null : h))}
                >
                  {(isCurrent || match) && <circle className="graph-halo" r={n.r + 5} />}
                  <circle className="graph-dot" r={n.r} />
                </g>
              );
            })}
            {/* Labels in a layer of their own, above every dot. */}
            <g className="graph-labels" style={{ fontSize: `${fontSize}px` }}>
              {sim.nodes.map((n) => {
                const spot = labelled.get(n.id);
                if (!spot) return null;
                const strong = hoverNode?.id === n.id || n.id === current || (!!q && matchIds.has(n.id));
                const cls = ["graph-label", strong && "strong", highlighting && !lit(n) && "dim"].filter(Boolean).join(" ");
                return (
                  <text key={n.id} className={cls} x={spot.x} y={spot.y} style={spot.anchor === "start" ? { textAnchor: "start" } : undefined}>
                    {n.label}
                  </text>
                );
              })}
            </g>
          </g>
        </g>
      </svg>

      {tooltip}

      {empty && (
        <div className="graph-empty">
          <EmptyState icon="graph" title={showChats ? "还没有可以画的笔记" : "知识图谱还是空的"}>
            笔记之间用 [[双链]] 连起来后，这里会画出它们的关系。
          </EmptyState>
        </div>
      )}

      <div className="graph-bar" role="toolbar" aria-label="知识图谱工具">
        <SearchInput
          size="sm"
          className="graph-search"
          value={query}
          onChange={setQuery}
          onEnter={onSearchEnter}
          placeholder="搜索并高亮"
          aria-label="搜索笔记节点"
        />
        {q && <span className={`graph-hits ${matches.length ? "" : "none"}`.trim()}>{matches.length ? `${matches.length} 个` : "无结果"}</span>}
        <i className="graph-sep" aria-hidden="true" />
        <label className="graph-switch" title="在图中显示 Chats/ 里的对话">
          <Toggle size="sm" checked={showChats} onChange={setShowChats} label="显示对话" />
          <span>对话</span>
        </label>
        <i className="graph-sep" aria-hidden="true" />
        <IconButton size="sm" icon="minus" label="缩小" onClick={() => zoomBy(0.8)} />
        <button type="button" className="graph-zoom" title="适应窗口（0）" onClick={fit}>
          {Math.round(view.k * 100)}%
        </button>
        <IconButton size="sm" icon="plus" label="放大" onClick={() => zoomBy(1.25)} />
        {currentNode && <IconButton size="sm" icon="pin" label={`定位到「${currentNode.title}」`} onClick={() => centreOn(currentNode, 1.2)} />}
      </div>

      <div className="graph-count" aria-live="polite">
        {state.loading && <Spinner size={11} />}
        {sim.nodes.length} 篇{showChats ? "笔记与对话" : "笔记"} · {sim.links.length} 条链接
      </div>

      {legend.length > 1 && (
        <div className="graph-legend" aria-label="分组">
          {legend.map(([name, info]) => (
            <button
              key={name}
              type="button"
              className={`graph-chip ${focusGroup === name ? "on" : ""} ${focusGroup && focusGroup !== name ? "off" : ""}`.trim()}
              style={{ "--node": info.colour }}
              aria-pressed={focusGroup === name}
              title={focusGroup === name ? "取消高亮" : `高亮「${name}」`}
              onClick={() => setFocusGroup((g) => (g === name ? null : name))}
            >
              <i aria-hidden="true" />
              {name}
              <em>{info.count}</em>
            </button>
          ))}
          {hiddenDomains > 0 && (
            <span className="graph-chip more" title={domains.slice(LEGEND_MAX).map(([name]) => name).join("、")}>
              +{hiddenDomains}
            </span>
          )}
        </div>
      )}

      <div className="graph-hint" aria-hidden="true">
        拖动平移 · 滚轮缩放 · 点击打开 · 双击空白处适应窗口
      </div>
    </div>
  );
}
