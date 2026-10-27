package server

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"marginalia/internal/config"
	"marginalia/internal/gitrepo"
	"marginalia/internal/knowledge"
)

// initCommitMessage is the subject of a new vault's first commit.
const initCommitMessage = "init: Marginalia 笔记库"

// starterFolders are the folders a new vault starts with (besides the
// assets folder, which comes from the settings). AGENTS.md explains them.
var starterFolders = []string{"Inbox", "Knowledge", knowledge.ChatsDir}

// starterGitignore keeps editor and OS clutter out of the vault's history
// (Obsidian's per-device workspace files, its trash, Finder metadata).
const starterGitignore = `.DS_Store
.trash/
.obsidian/workspace.json
.obsidian/workspace-mobile.json
`

// starterConventions is the AGENTS.md of a new vault: the conventions every
// AI (the app's assistant, the organize agent, MCP clients) reads before it
// changes notes. %[1]s is the assets folder, %[2]s today's date.
const starterConventions = `---
title: 知识库约定
summary: 本知识库的目录结构、笔记格式与链接规则；人和 AI 修改笔记前都要先读它。
tags: [meta]
type: reference
status: stable
updated: %[2]s
---

# 知识库约定

这是一个与 Obsidian 兼容的 Markdown 知识库，由 Marginalia 管理。
应用里的 AI 助手、「整理知识库」Agent，以及通过 MCP 接入的 Claude Code、Codex 等工具，
在修改任何笔记之前都会先读这份约定，并严格遵守。

## 目录结构

| 位置 | 用途 |
| --- | --- |
| ` + "`Inbox/`" + ` | 收件箱：随手记、摘录、还没想清楚的想法，等待整理 |
| ` + "`Knowledge/`" + ` | 知识正文：按主题建子文件夹（如 ` + "`Knowledge/机器学习/`" + `），一个主题一篇笔记 |
| ` + "`Chats/`" + ` | 与 AI 的对话记录，自动保存；` + "`status: inbox`" + ` 表示待沉淀，整理后变为 ` + "`distilled`" + `。不要手动改写 |
| ` + "`%[1]s/`" + ` | 图片与附件（上传的文件默认放在这里） |
| ` + "`index.md`" + ` | 知识地图：整个知识库的入口，链接到各个主题 |

## 笔记格式

每篇笔记以 YAML frontmatter 开头：

` + "```yaml" + `
---
title: 笔记标题
summary: 一两句话说明这篇笔记讲什么（AI 检索时最先看它，务必写准）
tags: [主题, 子主题]
type: note        # note 普通笔记 | concept 概念 | howto 操作指南 | reference 资料 | moc 主题地图
status: draft     # draft 草稿 | stable 已成熟
updated: 2026-01-01
---
` + "```" + `

- 文件名就是笔记标题，使用中文或英文均可，不要包含 ` + "`/ \\ : # ^ [ ] |`" + `。
- 一篇笔记只讲一个主题（原子化）；内容变长时拆成多篇，再互相链接。
- 修改笔记时同步更新 ` + "`updated`" + ` 日期；不要无故删除用户写过的内容。

## 链接与结构

- 笔记之间用双链：` + "`[[笔记名]]`" + `、` + "`[[笔记名#小节]]`" + `、` + "`[[笔记名|显示文字]]`" + `；图片用 ` + "`![[图片.png]]`" + `。
- 新建或移动笔记后，在 ` + "`index.md`" + ` 和相关主题笔记里加上链接，不要留下孤立笔记。
- 一个主题下的笔记多起来时，为它写一篇 ` + "`type: moc`" + ` 的主题地图，再从 ` + "`index.md`" + ` 链接过去。

## 写作

- 数学公式用 ` + "`$...$`" + ` 和 ` + "`$$...$$`" + `。
- 适合画图的地方使用 mermaid 代码块（流程图、思维导图、时序图等）。
- 引用外部资料时注明出处。
`

// starterIndex is the index.md (knowledge map) of a new vault. %[1]s is
// today's date.
const starterIndex = `---
title: 知识地图
summary: 知识库的入口，按主题链接到各篇笔记。
tags: [meta]
type: moc
status: stable
updated: %[1]s
---

# 知识地图

这是知识库的入口。每个主题一行，链接到它的笔记或主题地图，例如：

- [[知识库约定]]：目录结构与笔记格式

## 主题

（还没有内容。和 AI 聊完后，用「整理知识库」把对话沉淀为笔记，这里会自动更新。）
`

// starterFile is one file of a new vault.
type starterFile struct {
	path    string // vault-relative, slash-separated
	content string
}

// starterFiles returns the files of a new vault.
func starterFiles(assetsDir string, today time.Time) []starterFile {
	date := today.Format("2006-01-02")
	return []starterFile{
		{knowledge.ConventionsFile, fmt.Sprintf(starterConventions, assetsDir, date)},
		{knowledge.IndexFile, fmt.Sprintf(starterIndex, date)},
		{".gitignore", starterGitignore},
	}
}

// writeStarter creates the starter folders and files of a vault in dir and
// returns the files it created (vault-relative). Existing files are never
// overwritten, so adopting an existing Obsidian vault keeps its AGENTS.md.
func writeStarter(dir, assetsDir string, today time.Time) ([]string, error) {
	if assetsDir = strings.Trim(filepath.ToSlash(assetsDir), "/"); assetsDir == "" {
		assetsDir = "_assets"
	}
	for _, folder := range append(starterFolders, assetsDir) {
		if err := os.MkdirAll(filepath.Join(dir, filepath.FromSlash(folder)), 0o755); err != nil {
			return nil, fmt.Errorf("创建文件夹 %s 失败：%w", folder, err)
		}
	}
	var created []string
	for _, f := range starterFiles(assetsDir, today) {
		target := filepath.Join(dir, filepath.FromSlash(f.path))
		out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return created, fmt.Errorf("创建 %s 失败：%w", f.path, err)
		}
		_, werr := out.WriteString(f.content)
		if cerr := out.Close(); werr == nil {
			werr = cerr
		}
		if werr != nil {
			return created, fmt.Errorf("写入 %s 失败：%w", f.path, werr)
		}
		created = append(created, f.path)
	}
	return created, nil
}

// initRepo makes dir a git repository (unless it already is one) and
// commits files. Git problems are logged, never fatal: the vault works
// without history. It reports whether dir is a repository afterwards.
func initRepo(dir string, files []string, g config.GitSettings, log *slog.Logger) bool {
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		if err := gitrepo.Init(dir); err != nil {
			log.Warn("git init failed; the vault has no history", "dir", dir, "err", err)
			return false
		}
	}
	if len(files) > 0 {
		repo := gitrepo.New(gitrepo.Options{Root: dir, AuthorName: g.AuthorName, AuthorEmail: g.AuthorEmail, Enabled: true})
		repo.Record(files, initCommitMessage, true)
		repo.Close()
	}
	return true
}

// POST /api/app/vault/init {path}: creates (or adopts) a folder as a vault —
// the usual folders, a starter AGENTS.md and index.md, a git repository with
// a first commit — and opens it. Existing files are never overwritten.
// Answers the new settings plus {created, git}.
func (s *Server) initVault(w http.ResponseWriter, r *http.Request, st *state) error {
	var body struct {
		Path *string `json:"path"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		return err
	}
	if body.Path == nil || strings.TrimSpace(*body.Path) == "" {
		return required("path")
	}
	dir := strings.TrimSpace(*body.Path)
	if rest, ok := strings.CutPrefix(dir, "~/"); ok {
		if home, err := os.UserHomeDir(); err == nil {
			dir = filepath.Join(home, rest)
		}
	}
	if !filepath.IsAbs(dir) {
		return errorf(http.StatusBadRequest, "笔记库路径必须是绝对路径")
	}
	dir = filepath.Clean(dir)
	if info, err := os.Stat(dir); err == nil && !info.IsDir() {
		return errorf(http.StatusBadRequest, "笔记库路径不是文件夹")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return errorf(http.StatusBadRequest, "无法创建笔记库文件夹：%v", err)
	}
	dir, err := config.CheckNotesDir(dir)
	if err != nil {
		return errorf(http.StatusBadRequest, "%s", err.Error())
	}
	created, err := writeStarter(dir, st.settings.AssetsDir, s.now())
	if err != nil {
		return err
	}
	gitOK := initRepo(dir, created, st.settings.Git, s.log)
	if _, err := s.core.Update(func(next *config.Settings) error {
		next.NotesDir = dir
		return nil
	}); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, struct {
		settingsView
		Created []string `json:"created"`
		Git     bool     `json:"git"`
	}{s.settingsResponse(), orEmpty(created), gitOK})
	return nil
}
