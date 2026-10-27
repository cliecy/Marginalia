package desktop

import (
	"runtime"

	"github.com/egoist/mygo"
)

// fileManager is what the system calls its file manager, for menu labels.
func fileManager() string {
	switch runtime.GOOS {
	case "darwin":
		return "Finder"
	case "windows":
		return "资源管理器"
	default:
		return "文件管理器"
	}
}

// menuTemplate is the application menu, in Chinese like the rest of the
// app. Items that the page carries out emit MenuEvent with their command;
// standard items use MyGo's roles, relabelled (MyGo would label them in
// English, the only language it knows the app ships). On macOS the first
// menu is the app menu; elsewhere its items move to the File menu and the
// macOS-only roles are left out.
func (a *app) menuTemplate() []*mygo.MenuItem {
	mac := runtime.GOOS == "darwin"
	name := a.name
	command := func(label, accelerator string, c MenuCommand) *mygo.MenuItem {
		return &mygo.MenuItem{
			ID:          string(c),
			Label:       label,
			Accelerator: accelerator,
			Click:       func(*mygo.MenuItem, *mygo.Window) { a.command(c) },
		}
	}
	role := func(label string, r mygo.MenuRole) *mygo.MenuItem {
		return &mygo.MenuItem{Label: label, Role: r}
	}
	sep := mygo.Separator

	file := []*mygo.MenuItem{
		command("新建笔记", "CmdOrCtrl+N", CommandNewNote),
		command("打开笔记库…", "CmdOrCtrl+O", CommandOpenVault),
		sep(),
		command("在 "+fileManager()+" 中显示", "", CommandRevealVault),
		sep(),
		role("关闭窗口", mygo.RoleClose),
	}
	if !mac {
		file = append(file,
			sep(),
			command("设置…", "CmdOrCtrl+,", CommandSettings),
			sep(),
			role("退出", mygo.RoleQuit),
		)
	}

	edit := []*mygo.MenuItem{
		role("撤销", mygo.RoleUndo),
		role("重做", mygo.RoleRedo),
		sep(),
		role("剪切", mygo.RoleCut),
		role("拷贝", mygo.RoleCopy),
		role("粘贴", mygo.RolePaste),
	}
	if mac {
		edit = append(edit, role("粘贴并匹配样式", mygo.RolePasteAndMatchStyle))
	}
	edit = append(edit,
		role("删除", mygo.RoleDelete),
		role("全选", mygo.RoleSelectAll),
	)

	view := []*mygo.MenuItem{
		command("切换侧边栏", `CmdOrCtrl+\`, CommandToggleSidebar),
		command("搜索", "CmdOrCtrl+K", CommandSearch),
		sep(),
		role("重新载入", mygo.RoleReload),
	}
	if mygo.IsDev() {
		view = append(view, role("开发者工具", mygo.RoleToggleDevTools))
	}
	view = append(view,
		sep(),
		role("实际大小", mygo.RoleResetZoom),
		role("放大", mygo.RoleZoomIn),
		role("缩小", mygo.RoleZoomOut),
		sep(),
		role("切换全屏幕", mygo.RoleToggleFullScreen),
	)

	window := []*mygo.MenuItem{
		role("最小化", mygo.RoleMinimize),
		role("缩放", mygo.RoleZoom),
	}
	if mac {
		window = append(window, sep(), role("前置全部窗口", mygo.RoleFront))
	}

	help := []*mygo.MenuItem{
		{Label: "显示数据文件夹", Click: func(*mygo.MenuItem, *mygo.Window) { go a.revealDataDir() }},
		{Label: "显示日志文件夹", Click: func(*mygo.MenuItem, *mygo.Window) { go a.revealLogs() }},
	}

	var menus []*mygo.MenuItem
	if mac {
		menus = append(menus, &mygo.MenuItem{Label: name, Submenu: []*mygo.MenuItem{
			role("关于 "+name, mygo.RoleAbout),
			sep(),
			command("设置…", "Cmd+,", CommandSettings),
			sep(),
			role("服务", mygo.RoleServices),
			sep(),
			role("隐藏 "+name, mygo.RoleHide),
			role("隐藏其他", mygo.RoleHideOthers),
			role("全部显示", mygo.RoleUnhide),
			sep(),
			role("退出 "+name, mygo.RoleQuit),
		}})
	}
	windowMenu := &mygo.MenuItem{Label: "窗口", Submenu: window}
	helpMenu := &mygo.MenuItem{Label: "帮助", Submenu: help}
	if mac {
		// The Window menu lists the windows, the Help menu gets the search
		// field. Both roles are macOS-only: elsewhere MyGo would drop the
		// whole menu.
		windowMenu.Role, helpMenu.Role = mygo.RoleWindow, mygo.RoleHelp
	}
	return append(menus,
		&mygo.MenuItem{Label: "文件", Submenu: file},
		&mygo.MenuItem{Label: "编辑", Submenu: edit},
		&mygo.MenuItem{Label: "视图", Submenu: view},
		windowMenu,
		helpMenu,
	)
}
