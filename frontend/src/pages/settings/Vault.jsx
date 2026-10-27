import { useState } from "react";
import { useApp } from "../../app/AppContext.jsx";
import { formatCount } from "../../app/format.js";
import { api } from "../../lib/api.js";
import { baseName, isAbsolutePath, joinPath, tildePath, vaultNameProblem } from "../../lib/appApi.js";
import { pickFolder } from "../../lib/native.js";
import { Button, Icon, List, prompt, toast } from "../../ui/index.js";
import { SetHead, SetRow } from "./search.jsx";

/**
 * Starts the app over on the home page: a new vault changes everything the
 * shell holds (tree, open note, chats, history), so a fresh start is the
 * honest way to switch.
 */
function restartOnHome() {
  history.replaceState(null, "", "#/");
  location.reload();
}

/** Asks for an absolute folder on the server (browser hosts have no native picker). */
const askServerPath = (title, defaultValue = "") =>
  prompt({
    title,
    icon: "folder",
    label: "服务器上的文件夹（绝对路径）",
    defaultValue,
    placeholder: "/notes",
    mono: true,
    confirmLabel: "确定",
    validate: (value) => (isAbsolutePath(value) ? null : "请输入以 / 开头的绝对路径"),
  });

/**
 * 笔记库 — the vault folder: where it is, reveal it, switch to another
 * folder, or start a new one with the starter structure.
 */
export default function Vault() {
  const ctx = useApp();
  const native = ctx.host.native;
  const path = ctx.settings?.notes_dir || ctx.me.vault?.path || "";
  const name = ctx.me.vault?.name || baseName(path) || "笔记库";
  const [busy, setBusy] = useState("");
  const noteCount = ctx.files.filter((f) => /\.md$/i.test(f)).length;

  const restart = (message) => {
    toast(message, { duration: 0 });
    restartOnHome();
  };

  const change = async () => {
    const target = native ? await pickFolder("选择笔记库文件夹") : await askServerPath("更换笔记库", path);
    if (!target || target === path) return;
    setBusy("change");
    try {
      if (!(await ctx.flushEdits())) {
        toast.err("当前笔记没能保存，先处理保存失败再更换笔记库");
        setBusy("");
        return;
      }
      await ctx.updateSettings({ notes_dir: target });
      restart(`正在打开「${baseName(target)}」…`);
    } catch (err) {
      toast.err(`没能打开这个文件夹：${err.message}`);
      setBusy("");
    }
  };

  const create = async () => {
    const parent = native ? await pickFolder("新笔记库放在哪里？") : await askServerPath("新笔记库放在哪里？");
    if (!parent) return;
    const folder = await prompt({
      title: "给新笔记库起个名字",
      icon: "folder-plus",
      label: "名称",
      defaultValue: "Notes",
      hint: `会在 ${tildePath(parent)} 里新建这个文件夹，放好 Inbox、Knowledge、Chats、_assets 和一份写给 AI 的 AGENTS.md 约定，并初始化 git。`,
      validate: vaultNameProblem,
      confirmLabel: "创建",
    });
    if (!folder) return;
    const target = joinPath(parent, folder);
    setBusy("create");
    try {
      if (!(await ctx.flushEdits())) {
        toast.err("当前笔记没能保存，先处理保存失败再更换笔记库");
        setBusy("");
        return;
      }
      await api.initVault(target);
      restart(`已创建「${folder}」，正在打开…`);
    } catch (err) {
      toast.err(`没能创建笔记库：${err.message}`);
      setBusy("");
    }
  };

  return (
    <>
      <List>
        <SetRow
          icon={<span className="set-vault-ic"><Icon name="folder-open" size={17} /></span>}
          title={name}
          sub={
            <span className="set-path" title={path}>
              {tildePath(path) || "—"}
              {ctx.treeLoaded && <em className="tabnum"> · {formatCount(noteCount)} 篇笔记</em>}
            </span>
          }
          keywords={`笔记库 当前 路径 文件夹 vault ${path}`}
          env="notes_dir"
          className="set-vault"
          right={
            native && (
              <Button size="sm" icon="external" onClick={() => ctx.revealPath("")}>
                在 Finder 中显示
              </Button>
            )
          }
        />
        <SetRow
          title="更换笔记库"
          sub={native ? "打开另一个装着 Markdown 的文件夹；Obsidian 的库可以直接用" : "换成服务器上的另一个文件夹"}
          keywords="更换 打开 切换 文件夹 obsidian open"
          right={
            <Button size="sm" loading={busy === "change"} disabled={!!busy} onClick={change}>
              更换…
            </Button>
          }
        />
        <SetRow
          title="新建笔记库"
          sub="建好推荐的文件夹结构和约定，适合从零开始"
          keywords="新建 创建 初始化 new create init"
          right={
            <Button size="sm" loading={busy === "create"} disabled={!!busy} onClick={create}>
              新建笔记库…
            </Button>
          }
        />
      </List>

      <SetHead title="格式" />
      <List>
        <SetRow
          title="附件文件夹"
          sub="粘贴或拖进来的图片、PDF 会放在这里"
          keywords="附件 图片 上传 assets"
          right={<code className="set-code">{ctx.me.assets_dir || "_assets"}</code>}
        />
        <SetRow
          title="与 Obsidian 兼容"
          sub="[[双链]]、frontmatter、标注块和附件路径都按 Obsidian 的规则读写；.obsidian 等隐藏文件夹不会被改动"
          keywords="obsidian 兼容 wikilink 双链 frontmatter"
          right={<Icon name="check-circle" size={16} className="set-ok" />}
        />
      </List>
    </>
  );
}
