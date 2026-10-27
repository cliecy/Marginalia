# Marginalia 拾页

Marginalia 是一款 macOS 桌面应用：把一个与 Obsidian 兼容的 Markdown 文件夹变成带 AI 的个人知识库。你可以在其中阅读和编辑笔记、查看反向链接与知识图谱、和 AI 对话并让它整理知识库，每次修改都会记入 git 历史；同时它向本机的 AI 工具（Claude Code、Codex、Magpie 等）开放 REST、OpenAPI 与 MCP 接口。

> 界面风格受 [Magpie](https://github.com/yetone/magpie) 启发。

## 功能

- **笔记**：文件树、CodeMirror 编辑器、实时预览（KaTeX 公式、Mermaid 图表、代码高亮）、`[[双链]]` 与反向链接、全文搜索、附件上传。
- **知识图谱**：笔记之间的链接关系。
- **AI 对话**：携带笔记上下文提问；有自由提问的对话会自动保存到 `Chats/`（`status: inbox`）。
- **整理知识库**：点「沉淀到知识库」或侧边栏的「整理知识库」，AI 先制定计划，再逐篇撰写，生成可逐项勾选的差异预览；确认后写入并以 `ai: …` 提交到 git，对话标记为 `distilled`。所有 AI 操作都会读取笔记库根目录的 `AGENTS.md`（知识库约定）。
- **版本与同步**：笔记库是 git 仓库时自动提交（默认延迟 60 秒），可同步到 GitHub 等远端，使用应用生成的部署密钥。
- **外部接入**：本机 REST API、OpenAPI 文档、MCP（HTTP 与 stdio）；集成 [Magpie](https://github.com/yetone/magpie) 网关。
- **可选的无界面模式**：`marginalia serve` 以浏览器 + 密码登录的方式提供同一套界面和 API。

## 安装与构建

需要 macOS、Go 与 Node.js。桌面版由 [MyGo](https://github.com/egoist/mygo)（v0.6.4）驱动系统 WebView，界面是 `frontend/` 的 React 应用，不依赖 cgo。

```sh
npm --prefix frontend ci
go tool mygo build              # build/darwin-arm64/Marginalia.app 和 .dmg
go tool mygo build -skip-dmg    # 只生成 .app
```

`mygo build` 会先执行 `npm --prefix frontend run build:app`，把前端构建到 `internal/webui/dist`，再由 `go:embed` 打进二进制。当前构建采用临时（ad-hoc）签名，未经过 Apple 公证；首次打开需要在 Finder 中右键 →「打开」，或在「系统设置 → 隐私与安全性」中允许。

把 Marginalia.app 放进 `/Applications` 后，应用里的可执行文件也提供命令行模式：

```sh
APP="/Applications/Marginalia.app/Contents/MacOS/Marginalia"
"$APP" version
"$APP" serve --addr 127.0.0.1:8190
"$APP" mcp
```

## 数据目录与首次启动

数据目录是 `~/Library/Application Support/Marginalia/`（可用 `NOTES_DATA_DIR` 覆盖；从旧版 `MarkdownNote` 目录升级时会自动迁移一次）。密钥文件权限均为 0600：

| 文件 | 说明 |
| --- | --- |
| `settings.json` | 笔记库路径、AI 服务、本机 API、Git、外观；由设置页面修改 |
| `password` | 浏览器密码登录用，首次启动自动生成 |
| `session_secret` | 会话签名密钥，删除后所有登录失效 |
| `mcp_token` | REST API 与 MCP 共用的访问令牌；可在「API 与 MCP」页面重新生成 |
| `ai_api_key` | AI 服务的 API Key（在「设置 → AI 模型」填写；本机地址如 Magpie 可不填） |
| `ssh/id_ed25519` | git 远端同步用的部署密钥 |
| `api-endpoint.json` | App 运行时本机 API 的实际地址和进程号，退出时删除 |

日志在 `~/Library/Logs/Marginalia/`（菜单「帮助 → 显示日志文件夹」）。

首次启动：

1. 打开 Marginalia.app，引导页有两个选择：
   - **打开已有笔记库**：用系统文件夹选择框选中一个文件夹（例如现有的 Obsidian 库），直接使用，不改动任何文件；
   - **新建笔记库**：选一个位置并命名，App 会创建文件夹，写入 `Inbox/`、`Knowledge/`、`Chats/`、`_assets/`、`AGENTS.md`、`index.md`、`.gitignore`，并初始化 git 做第一次提交（已有文件从不覆盖）。
2. 在「设置 → AI 模型」填写 OpenAI 兼容服务的地址、Key 和默认模型，或在「Magpie」页面一键改用本机 Magpie。
3. 本机 API 默认开启在 `127.0.0.1:8190`（「设置 → API」可修改或关闭），「API 与 MCP」页面提供令牌和接入示例。

## AI 模型与 Magpie 集成

AI 功能使用任何 OpenAI 兼容的服务（「设置 → AI 模型」中填写地址、Key 与默认模型）。

[Magpie](https://github.com/yetone/magpie) 是运行在本机的 LLM 网关（`http://127.0.0.1:3425`，OpenAI 兼容接口在 `/v1`，Key 为 `magpie`，模型名如 `group/default`）。App 的「Magpie」页面：

- **检测**：读取网关的 `GET /` 和 `GET /v1/models`，显示是否运行、版本和可用模型数；网关运行时侧边栏的 Magpie 条目显示绿点。
- **使用 Magpie 作为 AI 来源**：一键把 AI 服务地址设为 `http://127.0.0.1:3425/v1`、Key 设为 `magpie`，默认模型优先选 `group/default`；之后对话和整理知识库都经由 Magpie。随时可在「设置 → AI 模型」改回。
- **把 Marginalia 添加到 Magpie 的 MCP 库**：调用 Magpie CLI（`/Applications/magpie.app/Contents/MacOS/magpie`，不需要在 PATH 里）执行 `magpie library mcp add marginalia <Marginalia 可执行文件> mcp`（stdio），或 HTTP 方式 `magpie library mcp add marginalia http://127.0.0.1:8190/mcp?token=…`；「移除」执行 `magpie library mcp rm marginalia`。这会修改 Magpie 交给各个 Agent 的配置，执行前界面会显示完整命令并请你确认。
- 没有安装 Magpie 时，页面给出安装链接，其余功能不受影响。

## 外部 API（REST 与 OpenAPI）

桌面 App 运行时，其他程序可以通过本机 API 使用和界面相同的 REST 接口：

- **地址**：默认 `http://127.0.0.1:8190`（端口被占用时依次尝试后面 20 个端口，以「API 与 MCP」页面显示的为准）；`marginalia serve` 下是 `--addr` 指定的地址。
- **鉴权**：`Authorization: Bearer <令牌>`。令牌就是数据目录里的 `mcp_token`，在「API 与 MCP」页面复制或重新生成（重新生成后旧令牌立即失效）。浏览器也可以用密码登录（`POST /api/auth/login`）后靠 cookie（`marginalia_session`）访问。
- **权限**：令牌可以访问笔记、搜索、反向链接、图谱、git 历史、AI 对话与整理等全部 `/api/*` 接口和 `/mcp`；`/api/app/*`（设置、令牌轮换、Magpie 操作等）只对 App 窗口和密码登录会话开放，用令牌访问返回 403。
- **OpenAPI**：`GET http://127.0.0.1:8190/api/openapi.json`（无需令牌），可导入 Postman、Bruno 等工具或用来生成客户端。

```sh
export MARGINALIA_URL=http://127.0.0.1:8190
export MARGINALIA_TOKEN='<令牌>'

# 全文搜索
curl -s "$MARGINALIA_URL/api/search?q=PPO" -H "Authorization: Bearer $MARGINALIA_TOKEN"
# 读取一篇笔记（返回 content 与 etag）
curl -s "$MARGINALIA_URL/api/note?path=index.md" -H "Authorization: Bearer $MARGINALIA_TOKEN"
# 新建笔记
curl -s -X POST "$MARGINALIA_URL/api/note" -H "Authorization: Bearer $MARGINALIA_TOKEN" \
  -H 'Content-Type: application/json' -d '{"path": "Inbox/来自脚本.md", "content": "# 来自脚本\n"}'
# 修改笔记：带上读取时的 etag，期间被别人改过则返回 409
curl -s -X PUT "$MARGINALIA_URL/api/note" -H "Authorization: Bearer $MARGINALIA_TOKEN" \
  -H 'Content-Type: application/json' -d '{"path": "Inbox/来自脚本.md", "content": "# 改过了\n", "base_etag": "<etag>"}'
```

通过 API 的修改与界面中的修改一样，会在提交延迟（默认 60 秒，「设置 → Git」可调）后自动提交到笔记库的 git。

## MCP

`POST /mcp`（Streamable HTTP，无状态，Bearer 令牌）。工具：`get_conventions`、`list_notes`、`search_notes`、`read_note`、`get_backlinks`、`write_note`、`append_to_note`、`move_note`、`delete_note`、`save_conversation`、`list_inbox`、`mark_chat_distilled`。

两种接入方式，工具完全相同：

- **HTTP**：`POST http://127.0.0.1:8190/mcp`，令牌放在 `Authorization: Bearer <令牌>` 头里；不能设置请求头的客户端可以改用 `http://127.0.0.1:8190/mcp?token=<令牌>`（令牌会出现在 URL 里，只在本机使用）。`GET /mcp` 返回 405（不提供服务端推送流）。
- **stdio**：`Marginalia mcp`（应用包里的可执行文件加 `mcp` 子命令）。它读取数据目录的设置：桌面 App 正在运行时，把每条消息带着数据目录里的令牌转发给 App，App 仍是笔记库唯一的写入者；App 没开时直接在进程内读写设置里的笔记库，之后 App 启动了会自动改为转发（先提交进程内的修改）。App 开着但关闭了本机 API 时，请求会返回错误，不会绕过 App 写笔记库。重新生成令牌、中途退出或重启 App 都不需要重启 `mcp`。stdout 只输出协议，日志写到 stderr。stdio 方式不需要手动配置令牌。

下面的路径以安装到 `/Applications` 为例；令牌在 App 的「API 与 MCP」页面复制（页面也会按当前地址和令牌生成这些片段）。

**Claude Code**

```sh
# stdio（推荐）
claude mcp add --scope user marginalia -- /Applications/Marginalia.app/Contents/MacOS/Marginalia mcp
# 或 HTTP
claude mcp add --scope user --transport http marginalia http://127.0.0.1:8190/mcp \
  --header "Authorization: Bearer <令牌>"
```

**Codex**（`~/.codex/config.toml`）

```toml
[mcp_servers.marginalia]
command = "/Applications/Marginalia.app/Contents/MacOS/Marginalia"
args = ["mcp"]
```

**Magpie**：在 App 的「Magpie」页面点「添加到 Magpie」，或自己执行

```sh
/Applications/magpie.app/Contents/MacOS/magpie library mcp add marginalia \
  /Applications/Marginalia.app/Contents/MacOS/Marginalia mcp
```

**其他客户端**（Cursor、Claude Desktop、Cherry Studio 等 `mcpServers` 格式）

```json
{ "mcpServers": { "marginalia": { "url": "http://127.0.0.1:8190/mcp", "headers": { "Authorization": "Bearer <令牌>" } } } }
```

> stdio 方式先访问数据目录里记录的地址（`api-endpoint.json`，记录它的进程已退出时忽略），没有时用设置里的 API 端口（默认 8190）。发送令牌之前，它先用 `GET /healthz?nonce=<随机数>` 让对方证明持有同一个令牌（回应里的 `proof` 是令牌对端口和随机数的 HMAC-SHA256，令牌本身不会发出）；证明不了的服务（例如 `marginalia serve`，或占用了该端口的其他程序）不会收到令牌，stdio 方式此时在进程内服务，并在 stderr 记一条警告。

## 可选：`marginalia serve`（自托管）

同一个可执行文件可以不开窗口，作为服务器运行：用浏览器访问同一套界面，密码登录（`password` 文件，首次启动自动生成；首页问候的名字用 `NOTES_LOGIN_USER` 设置，不设置时只显示问候语），API 与 MCP 行为与桌面版一致。收到 SIGINT/SIGTERM 时会等待请求结束并提交待提交的 git 修改。

```sh
NOTES_DIR=/path/to/notes NOTES_DATA_DIR=/path/to/data marginalia serve --addr 0.0.0.0:8190
```

- `--addr host:port`：监听地址，默认 `0.0.0.0:8190`；`--static <目录>`：用该目录的前端构建代替内嵌界面（同 `NOTES_STATIC_DIR`）。
- 与桌面版不同，服务器模式的默认值是：数据目录 `/data`、笔记库 `/notes`，没有预设的 AI 服务；健康检查在 `/healthz`。

| 环境变量 | 说明 |
| --- | --- |
| `NOTES_DIR`、`NOTES_DATA_DIR`、`NOTES_STATIC_DIR` | 笔记库、数据目录、前端目录 |
| `NOTES_LOGIN_USER`、`NOTES_SESSION_HOURS`、`NOTES_COOKIE_SECURE`、`NOTES_ALLOWED_ORIGINS` | 登录用户名、会话时长（默认 720 小时）、cookie 是否仅限 HTTPS、允许的跨源来源（逗号分隔） |
| `NOTES_AI_BASE_URL`、`NOTES_AI_API_KEY_FILE`、`NOTES_AI_DEFAULT_MODEL`、`NOTES_AI_VAULT_CHAR_BUDGET` | AI 服务地址、Key 文件、默认模型、笔记库上下文字符预算 |
| `NOTES_GIT_AUTOCOMMIT`、`NOTES_GIT_COMMIT_DELAY_SECONDS`、`NOTES_GIT_AUTOSYNC_MINUTES`、`NOTES_GIT_AUTHOR_NAME`、`NOTES_GIT_AUTHOR_EMAIL` | git 自动提交、提交延迟、自动同步间隔、提交作者 |
| `NOTES_ASSETS_DIR`、`NOTES_MAX_UPLOAD_MB` | 附件目录（默认 `_assets`）、上传大小上限（默认 50 MB） |
| `NOTES_FORWARDED_ALLOW_IPS` | 可信的反向代理（逗号分隔的 IP 或 CIDR，`*` 表示任何来源，空值表示不信任任何代理）；只有来自它们的请求才采用 `X-Forwarded-For`（登录限速按客户端地址计数）和 `X-Forwarded-Proto`（Origin 校验）。服务器模式默认 `127.0.0.1,::1`，桌面 App 默认不信任任何代理 |

这些环境变量对桌面版同样生效，并会覆盖 `settings.json` 中对应的值。未登录的请求（登录、静态页面等）请求体最多 64 KB、须在 30 秒内发完，登录请求最多 8 KB，并在读取请求体之前先检查登录限速。

## 开发与测试

```sh
npm --prefix frontend ci
go tool mygo dev        # 启动 Vite（npm run dev:app，端口 5173），编译并运行 “Marginalia Dev”，前端热更新、Go 代码改动后自动重启
```

- 修改 `Native` 的方法或事件后，`go tool mygo generate` 会重新生成 `frontend/src/lib/mygo.ts`（`mygo dev` / `mygo build` 也会自动执行）。
- 检查与测试：`go vet ./... && go tool mygo vet . && CGO_ENABLED=0 go test ./...`
- 前端构建检查：`cd frontend && npx vite build --outDir /tmp/marginalia-check --emptyOutDir`
- 开发版与正式版共用数据目录和设置；需要隔离时用 `NOTES_DATA_DIR=/某个/临时目录 go tool mygo dev`。

## 项目结构

```
 WebView 窗口 ──  mygo://localhost/…        ──►  AppHandler ─┐
                 （同源、无端口、免登录）                         │   server.Server
 其他程序 / MCP ── http://127.0.0.1:8190/… ──►  NetHandler ─┤   同一张路由表：
                 （Bearer 令牌或密码登录）                       │   /api/*、/mcp、/healthz、
 marginalia serve ── --addr 上的 TCP 监听 ──►  NetHandler ─┘   /api/openapi.json、静态页面
```

一个 `http.Handler`，两个入口：窗口内的 `mygo://` 协议页面与 API 同源，不占端口也不需要登录；同一套路由再挂在本机回环监听上供脚本、MCP 客户端使用。设置里修改 API 地址/端口或关闭 API 后，监听器会自动重启或停止。桌面外壳（`internal/desktop`）提供单实例、中文菜单、`marginalia://open?path=…` / `marginalia://search?q=…` 深链接，以及页面可调用的 `Native` 服务（选择文件夹、在 Finder 中显示、打开外部链接、应用信息）；退出前会让页面先保存未保存的修改。一个可执行文件、四个子命令：不带参数启动桌面 App，`serve`、`mcp`、`version`。

| 目录 | 内容 |
| --- | --- |
| `main.go` | 子命令入口 |
| `frontend/` | React + CodeMirror + KaTeX 前端 |
| `internal/config` | 默认值 → `settings.json` → `NOTES_*` 环境变量；密钥文件 |
| `internal/vault`、`knowledge`、`chats`、`gitrepo` | 笔记读写与链接、索引与图谱、AI 对话记录、git 历史与同步 |
| `internal/ai` | OpenAI 兼容客户端、带笔记上下文的对话、「整理知识库」Agent |
| `internal/mcp` | MCP 服务（HTTP、stdio、向 App 转发） |
| `internal/magpie` | Magpie 网关探测与 CLI 调用 |
| `internal/server` | 路由、鉴权、OpenAPI、API 监听器 |
| `internal/webui` | 内嵌的前端构建产物 |
| `internal/desktop` | MyGo 窗口、菜单、深链接、`Native` 服务 |
| `mygo.json`、`resources/icon.png` | MyGo 配置与应用图标（图标由 `go run ./scripts/genicon` 生成） |
| `docs/` | 宣传文案等 |
