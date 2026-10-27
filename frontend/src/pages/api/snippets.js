/**
 * Copyable examples for the API page (and the endpoint / tool details), built
 * from the live connection: the base URL, the MCP endpoint, the token and the
 * stdio command.
 *
 * The snippets that must carry the token itself (shell exports, Claude Code,
 * mcpServers JSON) hold the real value; the page masks it on screen and the
 * copy button copies the real text. Everything else reads it from
 * $MARGINALIA_TOKEN so it never sits in a script.
 */

/** Wraps a string as a single-quoted shell word. */
const shellQuote = (text) => (/^[\w@%+=:,./-]+$/.test(text) ? text : `'${String(text).replace(/'/g, `'\\''`)}'`);

/** A double-quoted TOML / JSON string. */
const quoted = (text) => JSON.stringify(String(text));

/**
 * Examples for the REST API.
 *
 * @param {{base: string, token: string}} conn
 */
export function restSnippets({ base, token }) {
  return [
    {
      id: "shell",
      label: "Shell",
      lang: "bash",
      code: `export MARGINALIA_URL=${shellQuote(base)}\nexport MARGINALIA_TOKEN=${shellQuote(token || "<令牌>")}`,
      note: "写进 ~/.zshrc 或工具的环境设置，后面几个示例都直接读这两个变量。",
    },
    {
      id: "curl",
      label: "curl",
      lang: "bash",
      code: [
        "# 全文搜索",
        `curl -s "$MARGINALIA_URL/api/search?q=PPO" \\`,
        `  -H "Authorization: Bearer $MARGINALIA_TOKEN"`,
        "",
        "# 读取一篇笔记",
        `curl -s "$MARGINALIA_URL/api/note?path=index.md" \\`,
        `  -H "Authorization: Bearer $MARGINALIA_TOKEN"`,
      ].join("\n"),
    },
    {
      id: "python",
      label: "Python",
      lang: "python",
      code: [
        "import os",
        "import requests",
        "",
        `BASE = os.environ.get("MARGINALIA_URL", ${quoted(base)})`,
        `HEADERS = {"Authorization": f"Bearer {os.environ['MARGINALIA_TOKEN']}"}`,
        "",
        `hits = requests.get(f"{BASE}/api/search", params={"q": "PPO"}, headers=HEADERS).json()`,
        `for hit in hits["results"]:`,
        `    print(hit["path"])`,
        "",
        `note = requests.get(f"{BASE}/api/note", params={"path": "index.md"}, headers=HEADERS).json()`,
        `print(note["content"])`,
      ].join("\n"),
      note: "需要 requests：pip install requests",
    },
    {
      id: "node",
      label: "Node",
      lang: "javascript",
      code: [
        `const BASE = process.env.MARGINALIA_URL ?? ${quoted(base)};`,
        "const headers = { Authorization: `Bearer ${process.env.MARGINALIA_TOKEN}` };",
        "",
        "const res = await fetch(`${BASE}/api/search?q=${encodeURIComponent(\"PPO\")}`, { headers });",
        "const { results } = await res.json();",
        "console.log(results.map((hit) => hit.path));",
        "",
        "// 新建一篇笔记",
        "await fetch(`${BASE}/api/note`, {",
        `  method: "POST",`,
        `  headers: { ...headers, "Content-Type": "application/json" },`,
        `  body: JSON.stringify({ path: "Inbox/来自脚本.md", content: "# 来自脚本\\n" }),`,
        "});",
      ].join("\n"),
      note: "Node 18 起自带 fetch；以 .mjs 保存即可使用顶层 await。",
    },
  ];
}

/**
 * Examples for MCP clients.
 *
 * @param {{mcpUrl: string, token: string, command?: string}} conn
 */
export function mcpSnippets({ mcpUrl, token, command }) {
  const bearer = `Bearer ${token || "<令牌>"}`;
  const tabs = [
    {
      id: "claude",
      label: "Claude Code",
      lang: "bash",
      code: `claude mcp add --transport http marginalia ${shellQuote(mcpUrl)} \\\n  --header ${quoted(`Authorization: ${bearer}`)}`,
      note: "加上 --scope user 后在所有项目里都能用；之后在 Claude Code 里输入 /mcp 查看状态。",
    },
  ];
  if (command) {
    tabs.push({
      id: "codex",
      label: "Codex",
      lang: "ini",
      code: `# ~/.codex/config.toml\n[mcp_servers.marginalia]\ncommand = ${quoted(command)}\nargs = ["mcp"]`,
      note: "stdio 方式不需要令牌：应用开着时转发给它，没开时直接读写笔记库。路径不对时换成应用的实际位置。",
    });
  }
  tabs.push(
    {
      id: "json",
      label: "JSON",
      lang: "json",
      code: JSON.stringify({ mcpServers: { marginalia: { url: mcpUrl, headers: { Authorization: bearer } } } }, null, 2),
      note: "Cursor、Claude Desktop、Cherry Studio 等使用 mcpServers 配置的客户端通用。",
    },
    {
      id: "curl",
      label: "curl",
      lang: "bash",
      code: [
        "# 列出知识库提供的工具",
        `curl -s ${shellQuote(mcpUrl)} \\`,
        `  -H "Authorization: Bearer $MARGINALIA_TOKEN" \\`,
        `  -H "Content-Type: application/json" \\`,
        `  -d '{"jsonrpc":"2.0","id":1,"method":"tools/list"}'`,
      ].join("\n"),
    },
  );
  return tabs;
}

/**
 * Examples for the OpenAPI document.
 *
 * @param {{openapiUrl: string}} conn
 */
export function openApiSnippets({ openapiUrl }) {
  return [
    {
      id: "curl",
      label: "curl",
      lang: "bash",
      code: `curl -s ${shellQuote(openapiUrl)} -o marginalia-openapi.json`,
    },
    {
      id: "generate",
      label: "生成客户端",
      lang: "bash",
      code: `npx @openapitools/openapi-generator-cli generate \\\n  -i ${shellQuote(openapiUrl)} \\\n  -g python -o marginalia-client`,
      note: "把 -g python 换成 typescript-fetch、go、rust… 即可生成其他语言的客户端。",
    },
  ];
}

/** A placeholder value for a JSON-schema type, for example bodies. */
function sampleValue(type, name) {
  if (type.endsWith("[]")) return [];
  if (type === "boolean") return false;
  if (type === "integer" || type === "number") return 0;
  if (type === "object") return {};
  if (name === "path") return "Inbox/示例.md";
  if (name === "content") return "# 示例\n";
  return "…";
}

/**
 * A curl call for one REST operation (from openApiOperations), reading the
 * address and token from the environment.
 */
export function curlForOperation(op) {
  const query = op.params
    .filter((p) => p.in === "query" && p.required)
    .map((p) => `${encodeURIComponent(p.name)}=${p.name === "path" ? "index.md" : p.name === "q" ? "PPO" : "…"}`)
    .join("&");
  const url = `$MARGINALIA_URL${op.path}${query ? `?${query}` : ""}`;
  const lines = [`curl -s${op.stream ? "N" : ""}${op.method === "GET" ? "" : ` -X ${op.method}`} "${url}"`, `-H "Authorization: Bearer $MARGINALIA_TOKEN"`];
  if (op.bodyType.startsWith("multipart/")) {
    for (const field of op.body) lines.push(field.type === "string" && field.name === "file" ? `-F "file=@./图片.png"` : `-F "${field.name}=…"`);
  } else if (op.bodyType) {
    const sample = Object.fromEntries(op.body.filter((f) => f.required || op.body.length <= 3).map((f) => [f.name, sampleValue(f.type, f.name)]));
    lines.push(`-H "Content-Type: application/json"`, `-d ${shellQuote(JSON.stringify(sample))}`);
  }
  return lines.join(" \\\n  ");
}

/** A tools/call JSON-RPC message for one MCP tool, with placeholder arguments. */
export function toolCallExample(tool) {
  const props = tool.inputSchema?.properties || {};
  const required = new Set(tool.inputSchema?.required || []);
  const args = Object.fromEntries(
    Object.entries(props)
      .filter(([name]) => required.has(name))
      .map(([name, schema]) => [name, sampleValue(Array.isArray(schema?.type) ? schema.type[0] : schema?.type || "string", name)]),
  );
  return JSON.stringify({ jsonrpc: "2.0", id: 1, method: "tools/call", params: { name: tool.name, arguments: args } }, null, 2);
}
