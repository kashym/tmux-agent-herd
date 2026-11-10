// Command herd is both the herdd daemon (`herd daemon`) and the CLI that
// humans and agents use to inspect and drive agent panes.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/kashym/tmux-agent-herd/internal/api"
	"github.com/kashym/tmux-agent-herd/internal/tmux"
)

// version is overridden at release time with -ldflags "-X main.version=...".
var version = "0.2.0" // keep in sync with VERSION

const usage = `herd — agent awareness for tmux

Usage: herd [-S socket | -L name] <command> [args]

Inspect:
  ls [--agents] [--json]                 list panes with agent state
  read <pane> [--lines N] [--json]       last N lines of a pane
  wait <pane> --for done[,idle,blocked] [--timeout S] [--json]
  log [pane] [--lines N] [--json]        recent state transitions

Drive:
  spawn [--right|--down|--window] [--name N] [--cwd DIR] [--keep] [--json] -- <cmd...>
  run [--right|--down|--window] [--name N] [--cwd DIR] [--json] <agent> [args...]
  send <pane> <text> [--enter] [--force] [--any-session]
  focus <pane> [--client C]
  close <pane> [--any-session]

UI (bound to keys by the plugin):
  pick [--client C]                      fzf picker of agent panes
  jump blocked|done [--client C]         go to the oldest blocked / unseen done pane
  sidebar [--toggle|--ensure] [--window W] [--width N]   live agent sidebar pane

Setup:
  setup <harness...|all> [--yes]         connect an agent harness to herd (shows a diff, asks first)
  setup --list                           supported harnesses: claude codex gemini copilot opencode pi aider
  setup --remove <harness...|all>        take herd back out of a harness's config
  install-skill [harness...|all] [--remove]   copy (or remove) the herd skill in each harness's skills dir
  daemon [--ensure] [--stop]             run (or make sure one is running) herdd
  hook [--agent A] [--msg M] [--kind K] [state]   used by scripts/herd-hook
  event --pane P --state S [--source hook|screen] [--agent A] [--kind K] [--msg M]
  version

<pane> is a pane id (%3), a tmux target (main:1.0) or a name given with --name.
`

type globals struct {
	tmuxSocket string
	tc         *tmux.Client
	herdSock   string
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	g := &globals{}
	for len(args) > 0 && strings.HasPrefix(args[0], "-") {
		switch args[0] {
		case "-S":
			if len(args) < 2 {
				return fail(stderr, errors.New("-S needs a socket path"))
			}
			g.tmuxSocket, args = args[1], args[2:]
		case "-L":
			if len(args) < 2 {
				return fail(stderr, errors.New("-L needs a socket name"))
			}
			g.tmuxSocket, args = tmux.SocketForName(args[1]), args[2:]
		case "-h", "--help", "help":
			fmt.Fprint(stdout, usage)
			return 0
		case "-V", "--version":
			fmt.Fprintln(stdout, "herd", version)
			return 0
		default:
			return fail(stderr, fmt.Errorf("unknown flag %s\n\n%s", args[0], usage))
		}
	}
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	if g.tmuxSocket == "" {
		g.tmuxSocket = os.Getenv("HERD_TMUX_SOCKET")
	}
	if g.tmuxSocket == "" {
		g.tmuxSocket = tmux.SocketFromEnv()
	}
	if g.tmuxSocket == "" {
		g.tmuxSocket = tmux.SocketForName("default")
	}
	g.tc = tmux.New(g.tmuxSocket)
	g.herdSock = api.SocketPath(g.tmuxSocket)

	cmd, rest := args[0], args[1:]
	var err error
	switch cmd {
	case "version":
		fmt.Fprintln(stdout, "herd", version)
	case "help":
		fmt.Fprint(stdout, usage)
	case "daemon":
		err = cmdDaemon(g, rest, stdout)
	case "ls", "list":
		err = cmdLs(g, rest, stdout)
	case "hook":
		err = cmdHook(g, rest)
	case "event":
		err = cmdEvent(g, rest)
	case "_focus":
		err = cmdFocusEvent(g, rest)
	case "spawn":
		err = cmdSpawn(g, rest, stdout, false)
	case "run":
		err = cmdSpawn(g, rest, stdout, true)
	case "send":
		err = cmdSend(g, rest)
	case "read":
		err = cmdRead(g, rest, stdout)
	case "wait":
		return cmdWait(g, rest, stdout, stderr)
	case "focus":
		err = cmdFocus(g, rest)
	case "close":
		err = cmdClose(g, rest)
	case "log":
		err = cmdLog(g, rest, stdout)
	case "pick":
		err = cmdPick(g, rest, stdout)
	case "_prompt":
		err = cmdPrompt(g, rest)
	case "jump":
		err = cmdJump(g, rest)
	case "sidebar":
		err = cmdSidebar(g, rest, stdout)
	case "setup":
		err = cmdSetup(g, rest, stdout)
	case "install-skill":
		err = cmdInstallSkill(g, rest, stdout)
	default:
		err = fmt.Errorf("unknown command %q (see herd help)", cmd)
	}
	if err != nil {
		return fail(stderr, err)
	}
	return 0
}

func fail(w io.Writer, err error) int {
	fmt.Fprintln(w, "herd:", err)
	return 1
}

// parseFlags parses flags that may appear anywhere among positional args
// (until "--"). Returns the positional args.
func parseFlags(fs *flag.FlagSet, args []string) ([]string, error) {
	fs.SetOutput(io.Discard)
	var flags, pos []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			pos = append(pos, args[i+1:]...)
			break
		}
		if len(a) > 1 && a[0] == '-' {
			name := strings.TrimLeft(a, "-")
			if eq := strings.IndexByte(name, '='); eq >= 0 {
				name = name[:eq]
			}
			f := fs.Lookup(name)
			if f == nil {
				return nil, fmt.Errorf("unknown flag %s", a)
			}
			flags = append(flags, a)
			if bf, ok := f.Value.(interface{ IsBoolFlag() bool }); (!ok || !bf.IsBoolFlag()) && !strings.Contains(a, "=") {
				if i+1 >= len(args) {
					return nil, fmt.Errorf("flag %s needs a value", a)
				}
				i++
				flags = append(flags, args[i])
			}
			continue
		}
		pos = append(pos, a)
	}
	if err := fs.Parse(flags); err != nil {
		return nil, err
	}
	return pos, nil
}

// pluginDir is the plugin root (the binary lives in <root>/bin/herd).
func pluginDir() string {
	exe, err := os.Executable()
	if err != nil {
		return "."
	}
	if r, err := filepath.EvalSymlinks(exe); err == nil {
		exe = r
	}
	return filepath.Dir(filepath.Dir(exe))
}

func selfExe() string {
	exe, err := os.Executable()
	if err != nil {
		return "herd"
	}
	return exe
}
