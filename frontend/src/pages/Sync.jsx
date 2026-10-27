import { useEffect, useMemo, useState } from "react";
import "./Sync.css";
import { useApp } from "../app/AppContext.jsx";
import { useAsync } from "../app/hooks.js";
import { formatCount, formatDateTime, noteTitle, timeAgo } from "../app/format.js";
import { api } from "../lib/api.js";
import { openExternal } from "../lib/native.js";
import { TopbarActions } from "../shell/Topbar.jsx";
import { Button, Card, CodeTabs, CopyField, EmptyState, Icon, IconButton, List, ListFoot, Page, PageIntro, Row, SectionHead, Select, Skeleton, Stats, StatusDot, Toggle, toast } from "../ui/index.js";

const AUTOSYNC_CHOICES = [0, 5, 10, 15, 30, 60, 120];

/**
 * git's sync stamps end in "+0900" (strftime %z); WebKit's Date only reads
 * "+09:00", so add the colon before formatting.
 */
const stamp = (value) => (typeof value === "string" ? value.replace(/([+-]\d{2})(\d{2})$/, "$1:$2") : value);
const HISTORY_ROWS = 5;
const KNOWN_HOSTS = /^(github\.com|gitlab\.com|gitee\.com|bitbucket\.org|codeberg\.org)$/i;

/**
 * Reads a git remote URL without ever showing credentials:
 * "git@github.com:me/notes.git" / "https://user:token@github.com/me/notes" →
 * { label: "github.com/me/notes", web: "https://github.com/me/notes", github: true }.
 */
function describeRemote(url) {
  if (!url) return null;
  let host = "";
  let path = "";
  const scp = /^(?:[^@/\s]+@)?([^:/\s]+):(?!\/)(.+)$/.exec(url);
  if (/^[a-z][a-z0-9+.-]*:\/\//i.test(url)) {
    try {
      const parsed = new URL(url);
      host = parsed.hostname;
      path = parsed.pathname.replace(/^\/+/, "");
    } catch {
      return { label: url.replace(/\/\/[^@/]*@/, "//"), web: null, github: false };
    }
  } else if (scp) {
    host = scp[1];
    path = scp[2];
  } else {
    return { label: url, web: null, github: false };
  }
  path = path.replace(/\.git$/i, "").replace(/\/+$/, "");
  const known = KNOWN_HOSTS.test(host);
  return { label: path ? `${host}/${path}` : host, web: known && path ? `https://${host}/${path}` : null, github: /^github\.com$/i.test(host) };
}

/** Seconds since `since` while `active`, ticking once a second. */
function useElapsed(active) {
  const [seconds, setSeconds] = useState(0);
  useEffect(() => {
    if (!active) return undefined;
    const start = Date.now();
    setSeconds(0);
    const timer = setInterval(() => setSeconds(Math.round((Date.now() - start) / 1000)), 1000);
    return () => clearInterval(timer);
  }, [active]);
  return seconds;
}

/** Re-renders every minute so "3 分钟前" stays true. */
function useMinuteTick() {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const timer = setInterval(() => setNow(Date.now()), 60000);
    return () => clearInterval(timer);
  }, []);
  return now;
}

/** A plain-language hint for the git errors people actually meet. */
function errorHint(message = "") {
  if (/冲突|conflict/i.test(message)) return "远端和本地改了同一处内容。本地内容没有受影响；可以在终端里进入笔记库手动合并（git pull --rebase），解决后再同步。";
  if (/permission denied|publickey|access denied|could not read from remote|authentication/i.test(message))
    return "远端拒绝了访问。确认下面的部署公钥已添加到仓库，并且勾选了写入权限（Allow write access）。";
  if (/could not resolve host|network|timed out|connection/i.test(message)) return "连不上远端仓库，检查一下网络后再试。";
  if (/origin/.test(message) && /没有配置|no such remote/i.test(message)) return "笔记库还没有配置名为 origin 的远端仓库。";
  if (/rejected|non-fast-forward|fetch first/i.test(message)) return "远端有本地没有的提交。再同步一次通常就能解决。";
  return null;
}

/** Status headline of the repository: dot tone, state text, and an explanation. */
function headline(git, syncing) {
  const last = git.last_sync;
  if (syncing) return { tone: "accent", pulse: true, state: "正在同步", sub: "提交本地修改，拉取远端，再推送" };
  if (!git.remote) return { tone: "grey", state: "仅本地", sub: "每次编辑都会提交到本地 git；配置远端仓库后可以一键同步" };
  if (last?.ok === false) return { tone: "red", state: "上次同步失败", sub: "本地内容没有受影响，看看下面的原因" };
  if (git.behind) return { tone: "amber", state: `远端有 ${git.behind} 个新提交`, sub: "同步会把它们拉取到本地" };
  if (git.ahead) return { tone: "amber", state: `${git.ahead} 个提交待推送`, sub: "同步会把本地的修改推送到远端" };
  return { tone: "green", state: last ? "已同步" : "已连接", sub: last ? "本地与远端一致" : "本次启动后还没有同步过" };
}

/** The last sync's outcome: success with what moved, or the error and what to do. */
function LastSync({ result, now, onOpen, files }) {
  if (!result) {
    return (
      <Card className="sync-last sync-last-none">
        <Icon name="cloud" size={16} />
        <span>本次启动后还没有同步过。点「立即同步」，或者开启下面的自动同步。</span>
      </Card>
    );
  }
  if (result.ok === false) {
    const hint = errorHint(result.error);
    return (
      <div className="sync-error" role="alert">
        <div className="sync-error-head">
          <Icon name="alert-circle" size={15} />
          <b>同步失败</b>
          <span className="sync-error-when" title={formatDateTime(result.at)}>
            {timeAgo(result.at, now)}
          </span>
        </div>
        {hint && <p className="sync-error-hint">{hint}</p>}
        {result.error && <pre className="sync-error-text">{result.error}</pre>}
      </div>
    );
  }
  const changed = result.changed || [];
  const moved = result.pulled || result.pushed;
  return (
    <List className="sync-last">
      <Row
        icon={
          <span className="sync-ok-ic">
            <Icon name="check" size={14} stroke={2} />
          </span>
        }
        title={moved ? "同步成功" : "已是最新"}
        sub={moved ? `拉取 ${formatCount(result.pulled || 0)} 个提交 · 推送 ${formatCount(result.pushed || 0)} 个提交` : "本地与远端没有差异"}
        meta={<span title={formatDateTime(result.at)}>{timeAgo(result.at, now)}</span>}
      />
      {changed.slice(0, 8).map((path) => {
        const exists = files.includes(path);
        return (
          <Row
            key={path}
            compact
            className="sync-changed"
            icon={/\.md$/i.test(path) ? "file-text" : "file"}
            title={noteTitle(path) || path}
            sub={path}
            meta={exists ? null : "已删除"}
            chevron={exists}
            onClick={exists ? () => onOpen(path) : undefined}
          />
        );
      })}
      {changed.length > 8 && <ListFoot>还有 {changed.length - 8} 个文件随同步更新</ListFoot>}
    </List>
  );
}

/**
 * 版本与同步 — the vault's git repository: where it stands against its remote,
 * a sync button with live progress, the last sync's outcome, the deploy key to
 * give the remote, automation, and the latest commits of the current note.
 * Shows a calm explanation when git is not enabled.
 *
 * Props: { route, sub }. Data: ctx.git (/api/git/status), ctx.syncing; actions ctx.sync(), ctx.reloadGit().
 */
export default function Sync() {
  const ctx = useApp();
  const now = useMinuteTick();
  const [checked, setChecked] = useState(ctx.git != null);
  const [savingGit, setSavingGit] = useState(false);
  const elapsed = useElapsed(ctx.syncing);
  const git = ctx.git;
  const remote = useMemo(() => describeRemote(git?.remote), [git?.remote]);

  useEffect(() => {
    let live = true;
    Promise.resolve(ctx.reloadGit()).finally(() => live && setChecked(true));
    ctx.reloadSettings();
    return () => {
      live = false;
    };
  }, []); // eslint-disable-line react-hooks/exhaustive-deps

  const lastAt = git?.last_sync?.at || "";
  const historyNote = ctx.lastNote && ctx.files.includes(ctx.lastNote) ? ctx.lastNote : null;
  const history = useAsync(() => (git?.enabled && historyNote ? api.history(historyNote) : Promise.resolve(null)), [git?.enabled, historyNote, lastAt]);

  const gitSettings = ctx.settings?.git;
  const updateGit = async (patch, message) => {
    setSavingGit(true);
    try {
      await ctx.updateSettings({ git: patch });
      await ctx.reloadGit();
      if (message) toast.ok(message);
    } catch (err) {
      toast.err(`没能保存：${err.message}`);
    } finally {
      setSavingGit(false);
    }
  };

  const vaultPath = ctx.me?.vault?.path || "";
  const cdLine = vaultPath ? `cd ${JSON.stringify(vaultPath)}` : "cd <笔记库目录>";

  const topbar = (
    <TopbarActions>
      <IconButton icon="refresh" label="刷新状态" onClick={() => ctx.reloadGit()} />
      <Button size="sm" variant="ghost" icon="settings" onClick={() => ctx.go("settings", "git")}>
        Git 设置
      </Button>
    </TopbarActions>
  );

  // ------------------------------------------------------------ not ready / not enabled
  if (!git) {
    return (
      <Page className="sync-page">
        {topbar}
        {!checked ? (
          <div className="sync-loading">
            <Card padded={false} className="sync-status">
              <Skeleton width={8} height={8} radius={4} />
              <div className="sync-status-who">
                <Skeleton width={120} height={11} />
                <Skeleton width="56%" height={8} />
              </div>
              <Skeleton width={84} height={27} radius={7} />
            </Card>
            <Stats loading items={[{ label: "待推送" }, { label: "待拉取" }, { label: "未提交" }, { label: "上次同步" }]} />
          </div>
        ) : (
          <EmptyState icon="alert-circle" title="读不到 git 状态" actions={<Button icon="refresh" onClick={() => ctx.reloadGit()}>重试</Button>}>
            服务暂时没有返回仓库信息。稍后再试，或者检查服务端日志。
          </EmptyState>
        )}
      </Page>
    );
  }

  if (!git.enabled) {
    return (
      <Page className="sync-page">
        {topbar}
        <EmptyState
          icon="git-branch"
          title="笔记库还没有启用 git"
          actions={
            <Button icon="settings" onClick={() => ctx.go("settings", "git")}>
              打开 Git 设置
            </Button>
          }
        >
          启用后每次编辑都会自动提交，任何版本都能找回，也能同步到 GitHub 等远端。在笔记库目录里运行下面的命令，然后重新打开应用；如果只是关了自动提交，在 Git 设置里打开即可。
        </EmptyState>
        <div className="sync-init">
          <CodeTabs tabs={[{ id: "init", label: "终端", lang: "bash", code: `${cdLine}\ngit init\ngit add -A && git commit -m "初始化笔记库"` }]} />
        </div>
      </Page>
    );
  }

  // ------------------------------------------------------------ enabled
  const head = headline(git, ctx.syncing);
  const last = git.last_sync ? { ...git.last_sync, at: stamp(git.last_sync.at) } : null;
  const unsaved = (git.uncommitted || 0) + (git.pending || 0);
  const autosync = gitSettings ? gitSettings.autosync_minutes : git.autosync_minutes;
  const autocommit = gitSettings?.autocommit;
  const delay = gitSettings?.commit_delay_seconds;

  const stats = [
    { label: "待推送", value: git.remote ? formatCount(git.ahead || 0) : "—", hint: git.remote ? "本地新提交" : "没有远端", tone: git.ahead ? "accent" : undefined },
    { label: "待拉取", value: git.remote ? formatCount(git.behind || 0) : "—", hint: git.remote ? "上次拉取后远端的新提交" : "没有远端", tone: git.behind ? "amber" : undefined },
    {
      label: "未提交",
      value: formatCount(unsaved),
      hint: git.pending ? `${git.pending} 个修改即将自动提交` : unsaved ? "工作区有改动" : "全部已提交",
    },
    {
      label: "上次同步",
      value: last ? timeAgo(last.at, now) || "—" : "—",
      hint: last ? (last.ok === false ? "失败" : formatDateTime(last.at)) : git.remote ? "还没有同步" : "仅本地",
      tone: last?.ok === false ? "red" : undefined,
    },
  ];

  const commits = history.data?.history || [];

  return (
    <Page className="sync-page">
      {topbar}
      <PageIntro>每次编辑都会自动提交到笔记库的 git 仓库，任何版本都能找回。配置远端仓库后，可以在多台设备之间同步。</PageIntro>

      <Card padded={false} className={`sync-status ${ctx.syncing ? "is-syncing" : ""}`.trim()}>
        <StatusDot tone={head.tone} pulse={head.pulse} label={head.state} />
        <div className="sync-status-who">
          <div className="sync-status-name">
            <b>git</b>
            <span className={`sync-state sync-state-${head.tone}`}>{ctx.syncing ? `正在同步 · ${elapsed} 秒` : head.state}</span>
          </div>
          <div className="sync-status-sub">
            <span className="sync-branch">
              <Icon name="git-branch" size={12} />
              {git.branch || "HEAD"}
            </span>
            <i aria-hidden="true">·</i>
            <span className="ellipsis">{head.sub}</span>
          </div>
        </div>
        {remote && (
          <button
            type="button"
            className="sync-remote"
            title={remote.web ? `在浏览器中打开 ${remote.label}` : remote.label}
            onClick={() => (remote.web ? openExternal(remote.web) : null)}
            disabled={!remote.web}
          >
            <code>{remote.label}</code>
            {remote.web && <Icon name="arrow-up-right" size={12} />}
          </button>
        )}
        {git.remote && (
          <Button variant="primary" icon="refresh" loading={ctx.syncing} onClick={ctx.sync} className="sync-now">
            {ctx.syncing ? "同步中" : "立即同步"}
          </Button>
        )}
        {ctx.syncing && <span className="sync-progress" aria-hidden="true" />}
      </Card>

      <Stats items={stats} className="sync-stats" />

      {git.remote ? (
        <>
          <SectionHead title="上次同步" />
          <LastSync result={last} now={now} files={ctx.files} onOpen={(path) => ctx.openNote(path)} />
        </>
      ) : (
        <>
          <SectionHead title="连接远端仓库" note="同步到 GitHub、Gitea 或任何 git 服务器" />
          <Card className="sync-connect">
            <ol className="sync-steps">
              <li>在 GitHub 等服务上新建一个私有仓库（不要勾选初始化 README）。</li>
              <li>把下面的部署公钥添加到仓库，勾选写入权限。</li>
              <li>在笔记库目录里添加远端并推送一次，之后就能在这里一键同步。</li>
            </ol>
            <CodeTabs
              tabs={[
                { id: "ssh", label: "SSH", lang: "bash", code: `${cdLine}\ngit remote add origin git@github.com:<用户名>/<仓库名>.git\ngit push -u origin ${git.branch || "main"}` },
                {
                  id: "gh",
                  label: "GitHub CLI",
                  lang: "bash",
                  code: `${cdLine}\ngh repo create notes --private --source . --remote origin --push`,
                  note: "需要先安装并登录 GitHub CLI（gh auth login）。",
                },
              ]}
            />
          </Card>
        </>
      )}

      <SectionHead title="部署公钥" note="远端仓库用它确认是这台设备在推送">
        {remote?.github && remote.web && (
          <Button size="sm" variant="ghost" iconRight="arrow-up-right" onClick={() => openExternal(`${remote.web}/settings/keys/new`)}>
            在 GitHub 添加
          </Button>
        )}
      </SectionHead>
      <Card className="sync-key">
        {git.public_key ? (
          <>
            <CopyField value={git.public_key} block className="sync-key-field" />
            <p className="sync-key-note">
              添加到仓库的 <b>Settings → Deploy keys</b>，勾选 <b>Allow write access</b>。远端地址请使用 SSH 形式（git@…）。私钥只保存在这台设备上。
            </p>
          </>
        ) : (
          <p className="sync-key-note">
            还没有部署密钥。应用启动时会自动生成；也可以使用系统里已有的 SSH 密钥或 HTTPS 凭据。
          </p>
        )}
      </Card>

      <SectionHead title="自动化" />
      <List>
        <Row
          icon="git-commit"
          title="自动提交"
          sub={
            autocommit === false
              ? "已关闭：修改不会自动提交"
              : delay != null
                ? `编辑停下约 ${delay >= 60 && delay % 60 === 0 ? `${delay / 60} 分钟` : `${delay} 秒`}后自动提交；整理、移动、删除会立即提交`
                : "编辑停下一会儿后自动提交；整理、移动、删除会立即提交"
          }
          right={gitSettings ? <Toggle checked={autocommit !== false} disabled={savingGit} label="自动提交" onChange={(on) => updateGit({ autocommit: on }, on ? "已开启自动提交" : "已关闭自动提交")} /> : null}
        />
        <Row
          icon="cloud-sync"
          title="自动同步"
          sub={
            !git.remote
              ? "配置远端仓库后可用"
              : autosync
                ? `每 ${autosync} 分钟在后台同步一次${gitSettings ? "" : "（由服务端的 NOTES_GIT_AUTOSYNC_MINUTES 设置）"}`
                : gitSettings
                  ? "关闭：只在你点「立即同步」时同步"
                  : "关闭（可以用服务端的 NOTES_GIT_AUTOSYNC_MINUTES 开启）"
          }
          right={
            gitSettings ? (
              <Select
                size="sm"
                width={120}
                disabled={savingGit || !git.remote}
                value={String(gitSettings.autosync_minutes || 0)}
                onChange={(value) => updateGit({ autosync_minutes: Number(value) }, Number(value) ? `每 ${value} 分钟自动同步` : "已关闭自动同步")}
                options={AUTOSYNC_CHOICES.map((m) => ({ value: String(m), label: m ? `每 ${m} 分钟` : "关闭" }))}
              />
            ) : null
          }
        />
      </List>

      {historyNote && (
        <>
          <SectionHead title="最近的提交" note={noteTitle(historyNote)}>
            <Button size="sm" variant="ghost" iconRight="chevron-right" onClick={() => ctx.openNote(historyNote)}>
              打开笔记
            </Button>
          </SectionHead>
          <List className="sync-history">
            {history.loading && !history.data ? (
              [0, 1, 2].map((i) => (
                <div key={i} className="row compact sync-skel">
                  <Skeleton width={26} height={26} radius={7} />
                  <div className="sync-skel-who">
                    <Skeleton width={`${52 - i * 8}%`} height={10} />
                    <Skeleton width={`${30 + i * 5}%`} height={8} />
                  </div>
                </div>
              ))
            ) : commits.length === 0 ? (
              <EmptyState compact icon="history" title="还没有提交记录">
                这篇笔记的修改提交后会出现在这里。
              </EmptyState>
            ) : (
              commits.slice(0, HISTORY_ROWS).map((commit) => (
                <Row
                  key={commit.rev}
                  compact
                  icon="git-commit"
                  title={commit.subject}
                  sub={
                    <>
                      <code className="sync-rev">{commit.rev.slice(0, 7)}</code>
                      {commit.author}
                    </>
                  }
                  meta={<span title={formatDateTime(commit.date)}>{timeAgo(commit.date, now)}</span>}
                />
              ))
            )}
            {commits.length > 0 && (
              <ListFoot onClick={() => ctx.openInspector("history")}>
                {commits.length > HISTORY_ROWS ? `在笔记旁查看全部 ${commits.length} 个版本` : "在笔记旁查看和恢复版本"}
              </ListFoot>
            )}
          </List>
        </>
      )}
    </Page>
  );
}
