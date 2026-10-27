import { useState } from "react";
import "./Onboarding.css";
import { api } from "../lib/api.js";
import { isAbsolutePath, joinPath, tildePath, vaultNameProblem } from "../lib/appApi.js";
import { isDesktop, pickFolder } from "../lib/native.js";
import { Button, Icon, IconButton, Input, Logo, Spinner } from "../ui/index.js";

/** What a new vault starts with (the server writes these in /api/app/vault/init). */
const STARTER = [
  { name: "Inbox/", note: "随手记，等着整理", folder: true },
  { name: "Knowledge/", note: "整理好的知识", folder: true },
  { name: "Chats/", note: "和 AI 的对话", folder: true },
  { name: "_assets/", note: "图片与附件", folder: true },
  { name: "AGENTS.md", note: "写给 AI 的约定" },
  { name: "index.md", note: "知识地图" },
];

/** One of the two big choices on the first screen. */
function Choice({ icon, title, children, busy, disabled, onClick }) {
  return (
    <button type="button" className="onb-choice" disabled={disabled} onClick={onClick}>
      <span className="onb-choice-ic">
        <Icon name={icon} size={18} />
      </span>
      <span className="onb-choice-text">
        <b>{title}</b>
        <small>{children}</small>
      </span>
      <span className="onb-choice-end">{busy ? <Spinner size={14} /> : <Icon name="chevron-right" size={14} />}</span>
    </button>
  );
}

/**
 * First run (desktop, or a server without a vault): pick the folder that holds
 * the notes, or make a new one with the starter structure. Native folder
 * pickers in the app; a typed absolute path when the page runs in a browser.
 *
 * @param {object} props
 * @param {object} props.me   /api/auth/me (vault.path is the last folder, when it went missing)
 * @param {() => Promise<void>} props.onDone reloads /api/auth/me; the app takes over once a vault is set
 */
export default function Onboarding({ me, onDone }) {
  const native = isDesktop;
  const [step, setStep] = useState("choose"); // choose | open-path | new
  const [busy, setBusy] = useState("");
  const [error, setError] = useState("");
  const [openPath, setOpenPath] = useState("");
  const [parent, setParent] = useState("");
  const [name, setName] = useState("Notes");
  const missing = me?.vault?.path;

  const fail = (err) => setError(err?.message || String(err));

  /** Selects an existing folder as the vault. */
  const open = async (path) => {
    setBusy("open");
    setError("");
    try {
      await api.updateAppSettings({ notes_dir: path });
      await onDone();
    } catch (err) {
      fail(err);
      setBusy("");
    }
  };

  const chooseExisting = async () => {
    setError("");
    if (!native) {
      setStep("open-path");
      return;
    }
    try {
      const path = await pickFolder("选择笔记库文件夹");
      if (path) await open(path);
    } catch (err) {
      fail(err);
    }
  };

  const chooseParent = async () => {
    setError("");
    try {
      const path = await pickFolder("新笔记库放在哪里？");
      if (path) {
        setParent(path);
        setStep("new");
      }
    } catch (err) {
      fail(err);
    }
  };

  const startNew = () => {
    setError("");
    if (native) chooseParent();
    else setStep("new");
  };

  const create = async (event) => {
    event.preventDefault();
    const problem = vaultNameProblem(name) || (!isAbsolutePath(parent) ? (native ? "先选一个位置" : "位置需要是服务器上的绝对路径") : null);
    if (problem) {
      setError(problem);
      return;
    }
    setBusy("new");
    setError("");
    try {
      await api.initVault(joinPath(parent.trim(), name.trim()));
      await onDone();
    } catch (err) {
      fail(err);
      setBusy("");
    }
  };

  const back = () => {
    setStep("choose");
    setError("");
  };

  const target = parent && name.trim() ? joinPath(parent.trim(), name.trim()) : "";

  return (
    <div className="gate onb">
      <div className="gate-card gate-wide onb-card">
        <Logo size={48} />
        <h1>{step === "new" ? "新建笔记库" : "选择你的笔记库"}</h1>
        <p className="gate-sub">
          {step === "new"
            ? "建好推荐的文件夹和一份写给 AI 的约定，马上就能开始写。"
            : "笔记库就是一个装着 Markdown 文件的文件夹。Marginalia 直接读写它，也可以和 Obsidian 同时打开同一个库。"}
        </p>
        {missing && step === "choose" && (
          <p className="onb-missing">
            <Icon name="alert" size={13} />
            <span>之前用的 {tildePath(missing)} 打不开了，换一个文件夹吧。</span>
          </p>
        )}

        {step === "choose" && (
          <div className="onb-choices" key="choose">
            <Choice icon="folder-open" title="打开已有文件夹" busy={busy === "open"} disabled={!!busy} onClick={chooseExisting}>
              在用 Obsidian 或别的工具？直接选那个文件夹。
            </Choice>
            <Choice icon="folder-plus" title="新建笔记库" disabled={!!busy} onClick={startNew}>
              选个位置，建好 Inbox、Knowledge、Chats 等文件夹。
            </Choice>
          </div>
        )}

        {step === "open-path" && (
          <form
            className="onb-form"
            key="open-path"
            onSubmit={(event) => {
              event.preventDefault();
              if (!isAbsolutePath(openPath.trim())) return setError("请输入服务器上的绝对路径，比如 /notes");
              open(openPath.trim());
            }}
          >
            <div className="onb-form-head">
              <IconButton icon="arrow-left" size="sm" label="返回" onClick={back} />
              <span>服务器上的文件夹</span>
            </div>
            <Input size="lg" mono autoFocus value={openPath} placeholder="/notes" onChange={(e) => (setOpenPath(e.target.value), setError(""))} aria-invalid={!!error || undefined} />
            <Button type="submit" variant="primary" size="lg" loading={busy === "open"} disabled={!openPath.trim()}>
              打开
            </Button>
          </form>
        )}

        {step === "new" && (
          <form className="onb-form" key="new" onSubmit={create}>
            <div className="onb-form-head">
              <IconButton icon="arrow-left" size="sm" label="返回" onClick={back} />
              <span>名称和位置</span>
            </div>
            <div className="onb-fields">
              <label className="onb-label" htmlFor="onb-name">
                名称
              </label>
              <Input id="onb-name" size="lg" autoFocus value={name} onChange={(e) => (setName(e.target.value), setError(""))} aria-invalid={!!error || undefined} />
              <label className="onb-label" htmlFor="onb-parent">
                位置
              </label>
              {native ? (
                <div className="onb-where">
                  <code id="onb-parent" title={parent}>
                    {tildePath(parent)}
                  </code>
                  <Button size="sm" onClick={chooseParent} disabled={!!busy}>
                    更改…
                  </Button>
                </div>
              ) : (
                <Input id="onb-parent" size="lg" mono value={parent} placeholder="/srv" onChange={(e) => (setParent(e.target.value), setError(""))} />
              )}
            </div>
            <div className="onb-tree" aria-label="将会创建">
              <div className="onb-tree-root">
                <Icon name="folder" size={14} />
                <code>{target ? `${tildePath(target)}/` : `${name.trim() || "Notes"}/`}</code>
              </div>
              {STARTER.map((item) => (
                <div key={item.name} className="onb-tree-item">
                  <Icon name={item.folder ? "folder" : "file-text"} size={13} />
                  <code>{item.name}</code>
                  <span>{item.note}</span>
                </div>
              ))}
            </div>
            <Button type="submit" variant="primary" size="lg" icon="check" loading={busy === "new"} disabled={!name.trim() || !parent.trim()}>
              创建并打开
            </Button>
          </form>
        )}

        {error && (
          <p className="onb-error" role="alert">
            {error}
          </p>
        )}

        <p className="gate-foot">所有内容都保存在你自己的{native ? "电脑" : "服务器"}上，并用 git 记录每一次修改。</p>
      </div>
    </div>
  );
}
