import { useApp } from "../../app/AppContext.jsx";
import { isDesktop, isMac } from "../../lib/native.js";
import { Button, Kbd, List, Segmented, Toggle } from "../../ui/index.js";
import { SetHead, SetRow } from "./search.jsx";

const THEMES = [
  { value: "system", label: "跟随系统" },
  { value: "light", label: "浅色" },
  { value: "dark", label: "深色" },
];

/** Shortcuts worth knowing; the desktop menu owns some of them. */
const SHORTCUTS = [
  { keys: "Mod+K", label: "搜索笔记", sub: "聚焦文件树的搜索框；回车做全文搜索" },
  { keys: "Mod+N", label: "新建笔记", sub: "放在当前笔记所在的文件夹", desktop: true },
  { keys: "Mod+S", label: "立即保存", sub: "平时停笔一秒多就会自动保存" },
  { keys: "Mod+\\", label: "显示或隐藏侧边栏" },
  { keys: "Mod+,", label: "打开设置" },
  { keys: "Mod+O", label: "打开笔记库", desktop: true },
];

/**
 * 常规 — appearance, the sidebar, the signed-in account (browser) and the
 * shortcuts.
 */
export default function General() {
  const ctx = useApp();
  const signedIn = !ctx.host.desktop && ctx.me?.user;
  return (
    <>
      <List>
        <SetRow
          title="外观"
          sub={`跟随系统时会随${isMac ? " macOS " : "系统"}的浅色 / 深色自动切换`}
          keywords="主题 深色 浅色 暗色 dark light theme appearance"
          right={<Segmented size="sm" value={ctx.theme} onChange={ctx.setTheme} options={THEMES} ariaLabel="外观" />}
        />
        <SetRow
          title="显示侧边栏"
          sub="也可以点顶栏左边的按钮，或按 ⌘\ 切换"
          keywords="侧边栏 sidebar 导航"
          right={<Toggle checked={!ctx.sidebarCollapsed} onChange={(on) => ctx.setSidebarCollapsed(!on)} label="显示侧边栏" />}
        />
        {signedIn && (
          <SetRow
            title="账号"
            sub={`已登录为 ${ctx.me.user}`}
            keywords="登录 退出 账户 logout"
            right={
              <Button size="sm" icon="logout" onClick={ctx.logout}>
                退出登录
              </Button>
            }
          />
        )}
      </List>

      <SetHead title="快捷键" />
      <List>
        {SHORTCUTS.filter((s) => !s.desktop || isDesktop).map((s) => (
          <SetRow key={s.keys} compact title={s.label} sub={s.sub} keywords={`快捷键 shortcut ${s.keys}`} right={<Kbd keys={s.keys} />} />
        ))}
      </List>
    </>
  );
}
