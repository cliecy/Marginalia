### 1. 小红书笔记

**标题**：
我做了个把 AI 对话变成笔记的开源 app✨

**正文**：
大家是不是也经常跟 AI 聊出许多宝藏干货，可关闭对话框后就再也找不到了？

最近开源了自己做的 macOS 知识库 App「Marginalia 拾页」，做它的初衷很简单：**你和 AI 的对话，就是学习最好的原材料。** 它能帮你把碎片对话无痛沉淀为结构化笔记和知识图谱。

它能做什么：
🌿 **轻巧原生**：Go + 系统 WebView 构建，安装包仅约 20MB！macOS 原生审美，浅色/深色界面克制又干净。
📝 **完全兼容 Obsidian**：原生 Markdown 存储，支持 [[双链]]、callout、KaTeX 公式、Mermaid 图，甚至可交互的 visual 图解。
🤖 **右侧 AI 助手**：随时针对当前笔记、选中文本或全库提问，一键润色、翻译、出题，甚至直接做成幻灯片。
🗂️ **安全整理**：对话自动存入 Chats/ 收件箱。「整理知识库」时，AI 会先列出修改建议，你可以逐条看 diff 确认后再写入，完全不担心笔记被篡改。
🔄 **自带时光机**：内置知识图谱与反向链接，每次修改自动 git 提交，随时能回滚。也支持用 `marginalia serve` 部署到 NAS 上，浏览器随时看。

更酷的是自带 REST API 和 MCP 协议，Cursor、Claude Code 这些 agent 都能直接读写你的知识库！

代码已完全开源，传送门：[GitHub 链接]

**标签**：
#知识管理 #笔记软件 #开源项目 #生产力工具 #Obsidian #AI工具 #独立开发 #Mac软件 #知识图谱

---

### 2. Twitter/X（英文）

**主推文（Main Tweet）**：
Most AI chats vanish into the void. But your conversations are the raw material for learning.

Meet Marginalia — an open-source, ~20MB native macOS app that turns your AI conversations into structured Markdown notes & knowledge graphs.

[GitHub 链接] 👇🧵

**Thread (4 条)**：

**1/4**
Native & Obsidian-compatible ✍️
Built with Go + system WebView (~20MB), clean macOS UI (dark/light).
Full Markdown support: [[wikilinks]], callouts, KaTeX math, Mermaid diagrams, and interactive visual charts. Every edit is auto-committed to git for instant rollback and sync.

**2/4**
AI as a true thinking partner 🧠
A dedicated AI panel queries active notes, selections, or your entire vault to polish, translate, generate quizzes, or build slides.
Chats auto-save to `Chats/`. Use "Organize Vault" to review AI diffs line-by-line before writing anything.

**3/4**
Built for the AI Agent era 🔌
Ships with REST API + OpenAPI docs and MCP (HTTP & stdio).
Your AI agents (Claude Code, Codex, Cursor) can directly read and write your vault. Connect Magpie to tap into local LLMs or distribute your knowledge base to any agent as an MCP.

**4/4**
Private, open & hackable 🚀
Run `marginalia serve` to host it on your NAS and access via browser anywhere. Zero lock-in, just pure Markdown on your local disk.

Star the repo and try it out:
[GitHub 链接]

---

### 3. 即刻 / V2EX 风格短帖

做了一款开源的 macOS 知识库桌面应用「Marginalia 拾页」。

核心想法是：我们每天与 AI 探讨的内容，是个人知识最好的原材料。拾页用 Go + 系统 WebView 构建，体积约 20MB，界面克制原生。笔记格式完全兼容 Obsidian（支持双链、KaTeX、Mermaid 及交互图解），每次修改自动 git 提交可随时回滚。

针对 AI 沉淀做了不少细节：对话自动归档到 Chats/ 收件箱；「整理知识库」时 AI 先出 diff 方案，确认后才写入；提供完整 REST API 与 MCP（stdio/HTTP），Claude Code、Cursor 等 agent 可直接双向读写库；支持接本地 Magpie 网关，也能一键 serve 部署到 NAS。

项目完全开源，欢迎大家体验和提 PR：[GitHub 链接]
