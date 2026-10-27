import { useApp } from "../../app/AppContext.jsx";
import { List, Select, toast } from "../../ui/index.js";
import { CommitInput, SettingToggle, intProblem } from "./controls.jsx";
import { SetHead, SetRow } from "./search.jsx";

const INTERVALS = [0, 5, 10, 15, 30, 60, 120];

/** "git@github.com:me/notes.git" → "github.com:me/notes". */
const shortRemote = (remote) => (remote || "").replace(/^(?:ssh:\/\/)?git@/, "").replace(/^https?:\/\//, "").replace(/\.git$/, "");

const EMAIL = /^[^\s@]+@[^\s@]+$/;

/**
 * Git — automatic commits (and their delay), periodic sync with the remote,
 * and who the commits are by.
 */
export default function Git() {
  const ctx = useApp();
  const git = ctx.settings?.git || {};
  const status = ctx.git;
  const remote = status?.remote ? shortRemote(status.remote) : "";
  const minutes = Number(git.autosync_minutes) || 0;
  const intervals = INTERVALS.includes(minutes) ? INTERVALS : [...INTERVALS, minutes].sort((a, b) => a - b);

  const save = (patch) => ctx.updateSettings({ git: patch });

  return (
    <>
      <List>
        <SetRow
          title="自动提交"
          sub="改动会自动提交到 git，任何版本都能在历史里找回"
          keywords="自动 提交 commit git 版本 历史"
          env="git.autocommit"
          right={<SettingToggle checked={!!git.autocommit} onSave={(autocommit) => save({ autocommit })} label="自动提交" />}
        />
        <SetRow
          title="提交延迟"
          sub="停笔多久后提交一次；这段时间里的连续修改会合成一个提交"
          keywords="延迟 提交 秒 delay"
          env="git.commit_delay_seconds"
          right={
            <CommitInput
              value={git.commit_delay_seconds ?? 60}
              width={104}
              suffix="秒"
              inputMode="numeric"
              className="tabnum"
              disabled={!git.autocommit}
              validate={intProblem(0, 86400, "延迟")}
              onCommit={(value) => save({ commit_delay_seconds: Number(value) })}
              aria-label="提交延迟（秒）"
            />
          }
        />
        <SetRow
          title="自动同步"
          sub={remote ? `定时与 ${remote} 拉取并推送` : "还没有配置远端仓库；配置好之后才能同步"}
          keywords="自动 同步 sync pull push 远端 remote"
          env="git.autosync_minutes"
          right={
            <Select
              size="sm"
              width={120}
              value={String(minutes)}
              searchable={false}
              options={intervals.map((m) => ({ value: String(m), label: m ? `每 ${m} 分钟` : "关闭" }))}
              onChange={(value) => save({ autosync_minutes: Number(value) }).catch((err) => toast.err(`没能保存：${err.message}`))}
              ariaLabel="自动同步间隔"
            />
          }
        />
      </List>

      <SetHead title="提交者" note="写进每个提交的 author / committer" />
      <List>
        <SetRow
          title="名字"
          keywords="作者 名字 author name"
          env="git.author_name"
          right={
            <CommitInput
              value={git.author_name ?? ""}
              width={200}
              placeholder="Marginalia"
              validate={(v) => (v ? null : "名字不能为空")}
              onCommit={(author_name) => save({ author_name })}
              aria-label="提交者名字"
            />
          }
        />
        <SetRow
          title="邮箱"
          keywords="作者 邮箱 email"
          env="git.author_email"
          right={
            <CommitInput
              value={git.author_email ?? ""}
              width={240}
              placeholder="you@example.com"
              validate={(v) => (!v ? "邮箱不能为空" : EMAIL.test(v) ? null : "邮箱格式不对")}
              onCommit={(author_email) => save({ author_email })}
              aria-label="提交者邮箱"
            />
          }
        />
      </List>

      <List className="set-gap">
        <SetRow
          icon="git-branch"
          title="版本与同步"
          sub={status?.enabled === false ? "这个笔记库还没有启用 git" : [status?.branch, remote || "没有远端"].filter(Boolean).join(" · ") || "查看状态、立即同步、部署公钥"}
          keywords="版本 同步 状态 公钥 deploy key"
          chevron
          onClick={() => ctx.go("sync")}
        />
      </List>
    </>
  );
}
