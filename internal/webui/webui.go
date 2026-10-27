// Package webui embeds the frontend build that the desktop app and
// `marginalia serve` serve.
//
// `npm --prefix frontend run build:app` writes the build into
// internal/webui/dist. A placeholder dist/.keep is committed so the package
// compiles before the frontend has been built; the server then shows
// FallbackHTML instead of the app.
package webui

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed all:dist
var dist embed.FS

// FS returns the frontend build: index.html, sandbox.html, assets/….
func FS() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil { // cannot happen: "dist" is a valid, embedded path
		panic(err)
	}
	return sub
}

// HasIndex reports whether the embedded build contains index.html, i.e.
// whether the frontend was built before the binary.
func HasIndex() bool { return HasIndexFS(FS()) }

// HasIndexFS reports whether fsys has a regular index.html at its root.
func HasIndexFS(fsys fs.FS) bool {
	if fsys == nil {
		return false
	}
	info, err := fs.Stat(fsys, "index.html")
	return err == nil && info.Mode().IsRegular()
}

// FallbackHTML is the page served instead of the app when the frontend was
// not built into the binary. It is self-contained (inline style only) so it
// renders under the app's Content-Security-Policy.
const FallbackHTML = `<!doctype html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Marginalia</title>
<style>
:root{color-scheme:light dark;--bg:#f4f4f6;--card:#fff;--fg:#1c1c21;--muted:#85858d;--line:#e3e3e8;--accent:#4f46e5;--pill:#f1f1f4}
@media (prefers-color-scheme:dark){:root{--bg:#1a1a1e;--card:#232327;--fg:#ededf1;--muted:#8b8b94;--line:#333339;--accent:#a5b4fc;--pill:#2d2d33}}
*{box-sizing:border-box}
body{margin:0;min-height:100vh;display:grid;place-items:center;background:var(--bg);color:var(--fg);
font:13px/1.5 -apple-system,BlinkMacSystemFont,"SF Pro Text","Segoe UI","PingFang SC",system-ui,sans-serif}
.card{width:min(440px,calc(100vw - 32px));background:var(--card);border:1px solid var(--line);border-radius:14px;padding:28px 28px 24px;
box-shadow:0 16px 44px rgba(20,22,30,.08),0 2px 8px rgba(20,22,30,.04)}
.logo{width:40px;height:40px;border-radius:9px;background:linear-gradient(135deg,#4f46e5,#6d64f0);display:grid;place-items:center;margin-bottom:14px}
h1{font-size:15px;font-weight:600;margin:0 0 6px}
p{margin:0 0 12px;color:var(--muted);font-size:12.5px}
code{font:12px ui-monospace,"SF Mono",Menlo,Consolas,monospace;background:var(--pill);border-radius:6px;padding:6px 8px;display:block;color:var(--fg);overflow-x:auto;white-space:nowrap}
.foot{margin-top:14px;font-size:11.5px}
</style>
</head>
<body>
<main class="card">
<div class="logo" aria-hidden="true"><svg width="22" height="22" viewBox="0 0 24 24" fill="none" stroke="#fff" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M4 19V5l8 8 8-8v14"/></svg></div>
<h1>界面还没有构建</h1>
<p>这个 Marginalia 程序里没有打包前端页面。在项目根目录运行下面的命令，然后重新编译：</p>
<code>npm --prefix frontend run build:app</code>
<p class="foot">API 不受影响：<code style="display:inline;padding:1px 5px">/api/openapi.json</code> 和 <code style="display:inline;padding:1px 5px">/mcp</code> 照常可用。</p>
</main>
</body>
</html>
`

// FallbackHandler serves FallbackHTML with 503 Service Unavailable (the web UI is
// "not built" yet).
func FallbackHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusServiceUnavailable)
		if r.Method != http.MethodHead {
			_, _ = w.Write([]byte(FallbackHTML))
		}
	})
}
