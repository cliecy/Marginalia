// Command marginalia is Marginalia: an Obsidian-compatible Markdown
// knowledge base with AI chat, as a desktop app, a headless server and an
// MCP server for local AI agents.
//
// Usage:
//
//	marginalia                       the desktop app (also: marginalia desktop)
//	marginalia serve [--addr host:port] [--static dir]
//	marginalia mcp                   MCP over stdio, for AI clients
//	marginalia version
//
// serve runs the same server without a window: it reads the NOTES_*
// environment variables (NOTES_DIR, NOTES_DATA_DIR, NOTES_AI_BASE_URL…) and
// the secret files of the data folder, and serves the web UI and the API on
// --addr (default 0.0.0.0:8190) until SIGINT or SIGTERM.
//
// mcp serves the Model Context Protocol on stdin/stdout. While the desktop
// app runs (its loopback API answers and proves it holds the data folder's
// API token), every message is relayed to it, so the app stays the only
// writer of the vault; otherwise the vault chosen in the app's settings
// (DataDir/settings.json, or NOTES_DATA_DIR and NOTES_DIR) is served
// in-process, handing over to the app once it starts. stdout carries the
// protocol only; logs go to stderr.
//
// The desktop app is built with `go tool mygo build` (see mygo.json); its
// typed frontend client, frontend/src/lib/mygo.ts, with `go tool mygo
// generate`.
package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime"
	"syscall"

	"marginalia/internal/config"
	"marginalia/internal/desktop"
	"marginalia/internal/mcp"
	"marginalia/internal/server"
	"marginalia/internal/webui"
)

// mygoJSON is the app's MyGo configuration: its name and version are the
// binary's, whatever builds it.
//
//go:embed mygo.json
var mygoJSON []byte

// version, when set with -ldflags "-X main.version=…", overrides the
// version of mygo.json.
var version string

// defaultServeAddr is where `serve` listens without --addr: every interface.
const defaultServeAddr = "0.0.0.0:8190"

// appInfo is what mygo.json says about the app.
type appInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// readAppInfo returns the app's name and version.
func readAppInfo() appInfo {
	info := appInfo{Name: "Marginalia", Version: mcp.DefaultVersion}
	var file appInfo
	if err := json.Unmarshal(mygoJSON, &file); err == nil {
		if file.Name != "" {
			info.Name = file.Name
		}
		if file.Version != "" {
			info.Version = file.Version
		}
	}
	if version != "" {
		info.Version = version
	}
	return info
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

// run runs the command line args and returns the exit status. The desktop
// app must run on the main goroutine, so run is called from main directly.
func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	info := readAppInfo()
	name, rest := parseCommand(args)
	var err error
	switch name {
	case "desktop":
		err = desktop.Run(desktop.Options{Name: info.Name, Version: info.Version})
	case "serve":
		err = serve(rest, info, stderr)
	case "mcp":
		err = serveMCP(rest, info, stdin, stdout, stderr)
	case "version":
		fmt.Fprintf(stdout, "%s %s (%s/%s)\n", info.Name, info.Version, runtime.GOOS, runtime.GOARCH)
	case "help":
		usage(stdout)
	default:
		fmt.Fprintf(stderr, "marginalia: unknown command %q\n\n", rest[0])
		usage(stderr)
		return 2
	}
	switch {
	case err == nil:
		return 0
	case errors.Is(err, flag.ErrHelp):
		return 0
	case errors.Is(err, errUsage):
		return 2
	default:
		fmt.Fprintf(stderr, "marginalia: %v\n", err)
		return 1
	}
}

// errUsage reports a command line mistake, already explained on stderr.
var errUsage = errors.New("usage")

// commandWord matches what looks like a subcommand rather than a deep link,
// a file or a flag the system passes to the app (macOS -psn_…).
var commandWord = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// parseCommand splits args into the command and its arguments. Without a
// command the desktop app runs and keeps every argument: the system starts
// it with deep links (marginalia://…) and files, which MyGo reads from the
// command line itself. A word that is no command is returned as "" with
// args unchanged.
func parseCommand(args []string) (string, []string) {
	if len(args) == 0 {
		return "desktop", nil
	}
	switch first := args[0]; first {
	case "desktop", "serve", "mcp", "version", "help":
		return first, args[1:]
	case "-h", "-help", "--help":
		return "help", args[1:]
	case "-version", "--version":
		return "version", args[1:]
	default:
		if commandWord.MatchString(first) {
			return "", args
		}
		return "desktop", args
	}
}

func usage(w io.Writer) {
	fmt.Fprint(w, `Usage:
  marginalia                        start the desktop app
  marginalia serve [flags]          serve the web UI and API (configured by NOTES_* environment variables)
      --addr host:port                address to listen on (default `+defaultServeAddr+`)
      --static dir                    serve the web UI from this folder instead of the built-in one
  marginalia mcp                    MCP server on stdin/stdout for AI clients
  marginalia version                print the version
`)
}

// newLogger returns a logger writing text to w and makes it the default.
func newLogger(w io.Writer) *slog.Logger {
	log := slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(log)
	return log
}

// serve is `marginalia serve`: the headless server, until SIGINT or
// SIGTERM, after which open requests drain and pending git edits are
// committed.
func serve(args []string, info appInfo, stderr io.Writer) error {
	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	flags.SetOutput(stderr)
	addr := flags.String("addr", defaultServeAddr, "address to listen on (`host:port`)")
	static := flags.String("static", "", "serve the web UI from this `folder` instead of the built-in one (like NOTES_STATIC_DIR)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() > 0 {
		fmt.Fprintf(stderr, "serve: unexpected argument %q\n", flags.Arg(0))
		flags.Usage()
		return errUsage
	}
	log := newLogger(stderr)

	settings, err := config.Load(config.ModeServer)
	if err != nil {
		return err
	}
	if *static != "" {
		dir, err := filepath.Abs(*static)
		if err != nil {
			return err
		}
		if !webui.HasIndexFS(os.DirFS(dir)) {
			return fmt.Errorf("serve: %s has no index.html", dir)
		}
		settings.StaticDir = dir
	}
	core, err := server.NewCore(settings, server.CoreOptions{
		Mode:      config.ModeServer,
		Version:   info.Version,
		DeployKey: true,
		Logger:    log,
	})
	if err != nil {
		return err
	}
	srv := server.New(core, server.Options{Logger: log})
	log.Info("starting Marginalia", "version", info.Version, "notes", settings.NotesDir, "data", settings.DataDir)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := srv.Serve(ctx, *addr); err != nil {
		return err
	}
	log.Info("stopped")
	return nil
}

// serveMCP is `marginalia mcp`: MCP over stdio, relayed to the running
// desktop app when it answers, else served in-process (see
// server.ServeMCPStdio).
func serveMCP(args []string, info appInfo, stdin io.Reader, stdout, stderr io.Writer) error {
	if len(args) > 0 {
		fmt.Fprintf(stderr, "mcp: unexpected argument %q\n", args[0])
		return errUsage
	}
	log := newLogger(stderr)
	load := func() (config.Settings, error) { return config.Load(config.ModeDesktop) }
	if _, err := load(); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return ignoreCancel(server.ServeMCPStdio(ctx, stdin, stdout, server.StdioOptions{
		Load:    load,
		Version: info.Version,
		Logger:  log,
	}))
}

// ignoreCancel treats the end of a session by a signal as a clean exit.
func ignoreCancel(err error) error {
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}
