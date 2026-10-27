import { existsSync, writeFileSync } from "node:fs";
import { join, resolve } from "node:path";
import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// The desktop build (`npm run build:app`) writes into the Go package
// internal/webui/dist, which keeps a committed placeholder `.keep` so that
// `//go:embed all:dist` compiles before the frontend is built. `--emptyOutDir`
// deletes it; this plugin puts it back once the bundle is written.
const webuiDist = resolve(import.meta.dirname, "../internal/webui/dist");

function keepPlaceholder() {
  let outDir = "";
  return {
    name: "marginalia:keep-placeholder",
    apply: "build",
    configResolved(config) {
      outDir = resolve(config.root, config.build.outDir);
    },
    closeBundle() {
      const keep = join(outDir, ".keep");
      if (outDir === webuiDist && !existsSync(keep)) writeFileSync(keep, "");
    },
  };
}

export default defineConfig({
  plugins: [react(), keepPlaceholder()],
  build: { sourcemap: false, chunkSizeWarningLimit: 2000 },
  server: { proxy: { "/api": "http://127.0.0.1:8190" } },
});
