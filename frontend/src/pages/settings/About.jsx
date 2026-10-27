import { useEffect, useState } from "react";
import { useApp } from "../../app/AppContext.jsx";
import { tildePath } from "../../lib/appApi.js";
import { appInfo, openExternal, revealDataDir } from "../../lib/native.js";
import { Button, CopyButton, Icon, List, Logo, Pill, toast } from "../../ui/index.js";
import { SetBlock, SetHead, SetRow } from "./search.jsx";

const OS_NAMES = { darwin: "macOS", windows: "Windows", win32: "Windows", linux: "Linux" };

const LINKS = [
  { icon: "magpie", title: "Magpie", sub: "yetone 的本地模型网关；这个界面的设计语言也来自它", url: "https://github.com/yetone/magpie" },
  { icon: "zap", title: "MyGo", sub: "用 Go 和系统 WebView 构建桌面应用的框架", url: "https://github.com/egoist/mygo" },
  { icon: "book-open", title: "Obsidian", sub: "同一个笔记库可以同时用 Obsidian 打开", url: "https://obsidian.md" },
  { icon: "plug", title: "Model Context Protocol", sub: "让 AI 工具读写知识库的开放协议", url: "https://modelcontextprotocol.io" },
];

/**
 * 关于 — what is running (version, platform, how it is hosted), where its data
 * lives, and the projects it stands on.
 */
export default function About() {
  const ctx = useApp();
  const [info, setInfo] = useState(null);
  useEffect(() => {
    let live = true;
    appInfo()
      .then((value) => live && setInfo(value))
      .catch(() => {});
    return () => {
      live = false;
    };
  }, []);

  const version = ctx.host.version || info?.version || "";
  const platform = info ? `${OS_NAMES[info.os] || info.os} ${info.arch}`.trim() : "";
  const mode = ctx.host.desktop ? "桌面应用" : "marginalia serve";
  const modeSub = ctx.host.desktop ? "Go + 系统 WebView（MyGo），单个小巧的应用" : "无界面的服务器模式 · 在浏览器中打开";
  const dataDir = ctx.settings?.data_dir || info?.dataDir || "";
  const diagnostics = [`Marginalia ${version || "(unknown)"}`, mode, platform, navigator.userAgent].filter(Boolean).join("\n");

  const reveal = async () => {
    try {
      await revealDataDir();
    } catch (err) {
      toast.err(err.message || String(err));
    }
  };

  return (
    <>
      <SetBlock keywords="关于 版本 marginalia about version">
        <div className="card set-about">
          <Logo size={48} />
          <div className="set-about-text">
            <h2>Marginalia</h2>
            <p>和 AI 一起整理的 Markdown 知识库</p>
          </div>
          <div className="set-about-meta">
            {version && <Pill tone="accent">v{version}</Pill>}
            <Pill>{mode}</Pill>
          </div>
        </div>
      </SetBlock>

      <List>
        <SetRow
          title="版本"
          sub={[version ? `Marginalia ${version}` : "版本未知", platform].filter(Boolean).join(" · ")}
          keywords="版本 version 平台 系统"
          right={<CopyButton text={diagnostics} label="复制版本信息" />}
        />
        <SetRow title="运行方式" sub={`${mode} · ${modeSub}`} keywords="运行 模式 mode 桌面 服务器" />
        {dataDir && (
          <SetRow
            title="数据文件夹"
            sub={<span className="set-path" title={dataDir}>{tildePath(dataDir)}</span>}
            keywords="数据 文件夹 目录 设置 令牌 data"
            right={
              ctx.host.native && (
                <Button size="sm" icon="external" onClick={reveal}>
                  在 Finder 中显示
                </Button>
              )
            }
          />
        )}
      </List>
      {dataDir && <p className="set-note">设置、访问令牌、AI 密钥和 Git 部署密钥都保存在数据文件夹里；笔记本身只在你的笔记库中。</p>}

      <SetHead title="相关项目" />
      <List>
        {LINKS.map((link) => (
          <SetRow
            key={link.url}
            icon={link.icon}
            title={link.title}
            sub={link.sub}
            keywords={link.url}
            meta={<Icon name="arrow-up-right" size={14} className="set-ext" />}
            onClick={() => openExternal(link.url)}
          />
        ))}
      </List>
    </>
  );
}
