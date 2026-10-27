import { Suspense, lazy, useEffect, useState } from "react";
import "./Graph.css";
import { useApp } from "../app/AppContext.jsx";
import { TopbarActions } from "../shell/Topbar.jsx";
import { Button, IconButton, Page, Spinner } from "../ui/index.js";

// The canvas and d3-force load with the page that needs them, not with the app.
const GraphView = lazy(() => import("../components/GraphView.jsx"));

/**
 * 知识图谱 — the whole vault as a graph, full-bleed under the top bar. The
 * canvas, its floating toolbar and legend are GraphView's; the page marks the
 * last opened note, opens notes on click and reloads when the vault changes.
 *
 * Props: { route, sub }.
 */
export default function Graph() {
  const ctx = useApp();
  const [reloadKey, setReloadKey] = useState(0);

  // Files changed outside the editor (sync, organize, uploads): draw again.
  useEffect(() => ctx.events.on("vault:changed", () => setReloadKey((k) => k + 1)), [ctx.events]);

  return (
    <Page width="full" flush className="gp">
      <TopbarActions>
        <IconButton icon="refresh" label="重新绘制" onClick={() => setReloadKey((k) => k + 1)} />
        <Button variant="soft" icon="sparkles" onClick={() => ctx.startOrganize({ preset: "health" })} title="让 AI 补上缺失的双链、更新知识地图">
          补全双链
        </Button>
      </TopbarActions>
      <div className="gp-canvas">
        <Suspense
          fallback={
            <div className="graph">
              <div className="graph-loading">
                <Spinner size={16} />
                <span>正在绘制知识图谱…</span>
              </div>
            </div>
          }
        >
          <GraphView current={ctx.lastNote} reloadKey={reloadKey} onOpen={(path) => ctx.openNote(path)} />
        </Suspense>
      </div>
    </Page>
  );
}
