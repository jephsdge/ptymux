package app

import (
	"errors"
	"flag"
	"fmt"
	"strings"
	"time"
)

type Action string

const (
	ActionRun     Action = "run"
	ActionHelp    Action = "help"
	ActionDaemon  Action = "daemon"
	ActionList    Action = "list"
	ActionKill    Action = "kill"
	ActionStop    Action = "stop"
	ActionIdle    Action = "idle"
	ActionSend    Action = "send"
	ActionText    Action = "text"
	ActionCommand Action = "command"
	ActionKeys    Action = "keys"
	ActionCtrlC   Action = "ctrl-c"
	ActionRead    Action = "read"
	ActionFollow  Action = "follow"
)

type Config struct {
	Action    Action
	Session   string
	Pane      string
	Tab       string
	Command   string
	Socket    string
	Follow    bool
	Wait      time.Duration
	ReadCount int
}

func Parse(args []string) (Config, error) {
	cfg := Config{
		Action:  ActionRun,
		Session: "default",
		Pane:    "default",
		Tab:     "default",
	}

	if len(args) > 0 && isHelpArg(args[0]) {
		cfg.Action = ActionHelp
		return cfg, nil
	}

	if len(args) > 0 {
		switch args[0] {
		case "help":
			cfg.Action = ActionHelp
			args = args[1:]
		case "daemon":
			cfg.Action = ActionDaemon
			args = args[1:]
		case "list":
			cfg.Action = ActionList
			args = args[1:]
		case "kill":
			cfg.Action = ActionKill
			args = args[1:]
		case "stop":
			cfg.Action = ActionStop
			args = args[1:]
		case "idle":
			cfg.Action = ActionIdle
			args = args[1:]
		case "send":
			cfg.Action = ActionSend
			args = args[1:]
		case "text":
			cfg.Action = ActionText
			args = args[1:]
		case "command":
			cfg.Action = ActionCommand
			args = args[1:]
		case "keys":
			cfg.Action = ActionKeys
			args = args[1:]
		case "ctrl-c":
			cfg.Action = ActionCtrlC
			args = args[1:]
		case "read":
			cfg.Action = ActionRead
			args = args[1:]
		case "follow":
			cfg.Action = ActionFollow
			args = args[1:]
		}
	}

	switch cfg.Action {
	case ActionHelp:
		return cfg, nil
	case ActionKill:
		return parseKill(cfg, args)
	case ActionSend:
		return parseSend(cfg, args)
	case ActionText:
		return parseText(cfg, args)
	case ActionCommand:
		return parseCommand(cfg, args)
	case ActionKeys:
		return parseKeys(cfg, args)
	case ActionIdle:
		return parseIdle(cfg, args)
	case ActionRead:
		return parseRead(cfg, args)
	case ActionFollow:
		return parseFollow(cfg, args)
	case ActionCtrlC:
		return parseTargetAction(cfg, args, "ctrl-c")
	}

	fs := flag.NewFlagSet("ptymux", flag.ContinueOnError)
	fs.StringVar(&cfg.Session, "s", cfg.Session, "session name")
	fs.StringVar(&cfg.Pane, "p", cfg.Pane, "pane name")
	fs.StringVar(&cfg.Tab, "t", cfg.Tab, "tab name")
	fs.StringVar(&cfg.Socket, "socket", "", "daemon socket path")
	if err := fs.Parse(args); err != nil {
		return Config{}, err
	}

	rest := fs.Args()
	if cfg.Action == ActionRun && len(rest) >= 1 {
		switch rest[0] {
		case "daemon":
			cfg.Action = ActionDaemon
			if len(rest) != 1 {
				return Config{}, fmt.Errorf("%s does not accept positional arguments", cfg.Action)
			}
			return cfg, nil
		case "list":
			cfg.Action = ActionList
			rest = rest[1:]
			if len(rest) > 1 {
				return Config{}, fmt.Errorf("%s accepts at most one target", cfg.Action)
			}
			if len(rest) == 1 {
				if err := applyTarget(&cfg, rest[0], true); err != nil {
					return Config{}, err
				}
			} else {
				cfg.Session = ""
				cfg.Pane = ""
				cfg.Tab = ""
			}
			return cfg, nil
		case "kill":
			cfg.Action = ActionKill
			rest = rest[1:]
			return parseKill(cfg, rest)
		case "stop":
			cfg.Action = ActionStop
			if len(rest) != 1 {
				return Config{}, fmt.Errorf("%s does not accept positional arguments", cfg.Action)
			}
			return cfg, nil
		case "idle":
			cfg.Action = ActionIdle
			rest = rest[1:]
			return parseIdle(cfg, rest)
		case "send":
			cfg.Action = ActionSend
			rest = rest[1:]
			return parseSend(cfg, rest)
		case "text":
			cfg.Action = ActionText
			rest = rest[1:]
			return parseText(cfg, rest)
		case "command":
			cfg.Action = ActionCommand
			rest = rest[1:]
			return parseCommand(cfg, rest)
		case "keys":
			cfg.Action = ActionKeys
			rest = rest[1:]
			return parseKeys(cfg, rest)
		case "ctrl-c":
			cfg.Action = ActionCtrlC
			rest = rest[1:]
			return applyTargetOnly(&cfg, rest, "ctrl-c")
		case "read":
			cfg.Action = ActionRead
			rest = rest[1:]
			return parseRead(cfg, rest)
		case "follow":
			cfg.Action = ActionFollow
			rest = rest[1:]
			return parseFollow(cfg, rest)
		}
	}

	if cfg.Action == ActionRun {
		if len(rest) == 0 {
			return Config{}, errors.New("missing command")
		}
		if len(rest) >= 2 {
			if err := applyTarget(&cfg, rest[0], false); err != nil {
				return Config{}, err
			}
			rest = rest[1:]
		}
		cfg.Command = strings.Join(rest, " ")
		return cfg, nil
	}

	if cfg.Action == ActionList && len(rest) <= 1 {
		if len(rest) == 1 {
			if err := applyTarget(&cfg, rest[0], true); err != nil {
				return Config{}, err
			}
		} else {
			cfg.Session = ""
			cfg.Pane = ""
			cfg.Tab = ""
		}
		return cfg, nil
	}

	if cfg.Action == ActionIdle {
		return parseIdle(cfg, rest)
	}

	if cfg.Action == ActionSend {
		return parseSend(cfg, rest)
	}

	if cfg.Action == ActionText {
		return parseText(cfg, rest)
	}

	if cfg.Action == ActionCommand {
		return parseCommand(cfg, rest)
	}

	if cfg.Action == ActionKeys {
		return parseKeys(cfg, rest)
	}

	if cfg.Action == ActionKill {
		return parseKill(cfg, rest)
	}

	if cfg.Action == ActionCtrlC {
		return applyTargetOnly(&cfg, rest, "ctrl-c")
	}

	if cfg.Action == ActionRead {
		return parseRead(cfg, rest)
	}

	if cfg.Action == ActionFollow {
		return parseFollow(cfg, rest)
	}

	if len(rest) != 0 {
		return Config{}, fmt.Errorf("%s does not accept positional arguments", cfg.Action)
	}
	return cfg, nil
}

func parseSend(cfg Config, args []string) (Config, error) {
	if hasHelpArg(args) {
		cfg.Action = ActionHelp
		return cfg, nil
	}
	fs := flag.NewFlagSet("send", flag.ContinueOnError)
	var waitValue string
	registerSocketFlag(fs, &cfg)
	fs.BoolVar(&cfg.Follow, "f", false, "follow output until interrupted")
	fs.StringVar(&waitValue, "t", "", "wait until PTY output is quiet for this duration")
	if err := fs.Parse(args); err != nil {
		return Config{}, err
	}
	if cfg.Follow && waitValue != "" {
		return Config{}, errors.New("send -f and -t cannot be used together")
	}
	if waitValue != "" {
		wait, err := parseWait(waitValue)
		if err != nil {
			return Config{}, err
		}
		cfg.Wait = wait
	}
	return applyCommandTarget(&cfg, fs.Args(), "send")
}

func parseText(cfg Config, args []string) (Config, error) {
	if hasHelpArg(args) {
		cfg.Action = ActionHelp
		return cfg, nil
	}
	fs := flag.NewFlagSet("text", flag.ContinueOnError)
	registerSocketFlag(fs, &cfg)
	if err := fs.Parse(args); err != nil {
		return Config{}, err
	}
	return applyCommandTarget(&cfg, fs.Args(), "text")
}

func parseKill(cfg Config, args []string) (Config, error) {
	if hasHelpArg(args) {
		cfg.Action = ActionHelp
		return cfg, nil
	}
	fs := flag.NewFlagSet("kill", flag.ContinueOnError)
	registerSocketFlag(fs, &cfg)
	if err := fs.Parse(args); err != nil {
		return Config{}, err
	}
	rest := fs.Args()
	switch len(rest) {
	case 0:
		cfg.Session = ""
		cfg.Pane = ""
		cfg.Tab = ""
		return cfg, nil
	case 1:
		if err := applyTarget(&cfg, rest[0], false); err != nil {
			return Config{}, err
		}
		return cfg, nil
	default:
		return Config{}, errors.New("kill accepts at most one target")
	}
}

func parseCommand(cfg Config, args []string) (Config, error) {
	if hasHelpArg(args) {
		cfg.Action = ActionHelp
		return cfg, nil
	}
	fs := flag.NewFlagSet("command", flag.ContinueOnError)
	var waitValue string
	registerSocketFlag(fs, &cfg)
	fs.BoolVar(&cfg.Follow, "f", false, "follow output until interrupted")
	fs.StringVar(&waitValue, "t", "", "wait until PTY output is quiet for this duration")
	if err := fs.Parse(args); err != nil {
		return Config{}, err
	}
	if cfg.Follow && waitValue != "" {
		return Config{}, errors.New("command -f and -t cannot be used together")
	}
	if waitValue != "" {
		wait, err := parseWait(waitValue)
		if err != nil {
			return Config{}, err
		}
		cfg.Wait = wait
	}
	return applyCommandTarget(&cfg, fs.Args(), "command")
}

func parseKeys(cfg Config, args []string) (Config, error) {
	if hasHelpArg(args) {
		cfg.Action = ActionHelp
		return cfg, nil
	}
	fs := flag.NewFlagSet("keys", flag.ContinueOnError)
	var waitValue string
	registerSocketFlag(fs, &cfg)
	fs.BoolVar(&cfg.Follow, "f", false, "follow output until interrupted")
	fs.StringVar(&waitValue, "t", "", "wait until PTY output is quiet for this duration")
	if err := fs.Parse(args); err != nil {
		return Config{}, err
	}
	if cfg.Follow && waitValue != "" {
		return Config{}, errors.New("keys -f and -t cannot be used together")
	}
	if waitValue != "" {
		wait, err := parseWait(waitValue)
		if err != nil {
			return Config{}, err
		}
		cfg.Wait = wait
	}
	return applyCommandTarget(&cfg, fs.Args(), "keys")
}

func parseIdle(cfg Config, args []string) (Config, error) {
	if hasHelpArg(args) {
		cfg.Action = ActionHelp
		return cfg, nil
	}
	fs := flag.NewFlagSet("idle", flag.ContinueOnError)
	waitValue := "500ms"
	registerSocketFlag(fs, &cfg)
	fs.StringVar(&waitValue, "t", waitValue, "wait until PTY output is quiet for this duration")
	if err := fs.Parse(args); err != nil {
		return Config{}, err
	}
	wait, err := parseWait(waitValue)
	if err != nil {
		return Config{}, err
	}
	cfg.Wait = wait
	return applyCommandTarget(&cfg, fs.Args(), "idle")
}

func parseRead(cfg Config, args []string) (Config, error) {
	if hasHelpArg(args) {
		cfg.Action = ActionHelp
		return cfg, nil
	}
	fs := flag.NewFlagSet("read", flag.ContinueOnError)
	fs.IntVar(&cfg.ReadCount, "n", 0, "number of recent command transcript entries to read")
	if err := fs.Parse(args); err != nil {
		return Config{}, err
	}
	return applyTargetOnly(&cfg, fs.Args(), "read")
}

func parseFollow(cfg Config, args []string) (Config, error) {
	if hasHelpArg(args) {
		cfg.Action = ActionHelp
		return cfg, nil
	}
	cfg.Follow = true
	return applyTargetOnly(&cfg, args, "follow")
}

func parseTargetAction(cfg Config, args []string, action string) (Config, error) {
	if hasHelpArg(args) {
		cfg.Action = ActionHelp
		return cfg, nil
	}
	fs := flag.NewFlagSet(action, flag.ContinueOnError)
	registerSocketFlag(fs, &cfg)
	if err := fs.Parse(args); err != nil {
		return Config{}, err
	}
	return applyTargetOnly(&cfg, fs.Args(), action)
}

func registerSocketFlag(fs *flag.FlagSet, cfg *Config) {
	fs.StringVar(&cfg.Socket, "socket", cfg.Socket, "daemon socket path")
}

func isHelpArg(arg string) bool {
	return arg == "-h" || arg == "--help"
}

func hasHelpArg(args []string) bool {
	for _, arg := range args {
		if isHelpArg(arg) {
			return true
		}
	}
	return false
}

func HelpText() string {
	return strings.TrimLeft(`
ptymux - persistent command-line PTY targets

Usage:
  ptymux [--socket PATH] <target> <command>
  ptymux [--socket PATH] idle [-t DURATION] <target> <input>
  ptymux [--socket PATH] send [-f | -t DURATION] <target> <input>
  ptymux [--socket PATH] text <target> <text>
  ptymux [--socket PATH] command [-f | -t DURATION] <target> <keys>
  ptymux [--socket PATH] keys [-f | -t DURATION] <target> <keys>
  ptymux [--socket PATH] read [-n N] <target>
  ptymux [--socket PATH] follow <target>
  ptymux [--socket PATH] list [target]
  ptymux [--socket PATH] kill [target]
  ptymux [--socket PATH] stop
  ptymux -h | --help | help

Targets:
  work             -> work/default/default
  work/main        -> work/main/default
  work/main/build  -> work/main/build

Examples:
  ptymux work "pwd"
  ptymux work "cd /tmp"
  ptymux send -t 500ms work "pwd"
  ptymux text work "hello world"
  ptymux command work "ctrl-c"
  ptymux keys work "up enter"
  ptymux keys -f work "pageup"
  ptymux read -n 3 work
  ptymux follow work
  ptymux kill work

Options:
  --socket PATH    use a custom daemon socket
  -f               follow output until interrupted
  -t DURATION      wait until PTY output is quiet; bare numbers are ms
  -n N             read the recent N terminal command regions

Default socket:
  ~/.ptymux/sockets/ptymux-default.sock

Output:
  clean text by default; terminal color/title/cursor controls are removed

Config:
  ~/.ptymux/config.json
  shell defaults to /bin/sh
  auto_release.enabled defaults to true
  auto_release.target_idle_timeout defaults to 8h
  auto_release.daemon_idle_timeout defaults to 30m
  restart with ptymux stop after changing daemon config
`, "\n")
}

func parseWait(value string) (time.Duration, error) {
	if value == "" {
		return 0, nil
	}
	if value[len(value)-1] >= '0' && value[len(value)-1] <= '9' {
		value += "ms"
	}
	wait, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("invalid duration %q: %w", value, err)
	}
	if wait <= 0 {
		return 0, fmt.Errorf("duration must be positive: %s", value)
	}
	return wait, nil
}

func applyTargetOnly(cfg *Config, rest []string, action string) (Config, error) {
	if len(rest) != 1 {
		return Config{}, fmt.Errorf("%s requires exactly one target", action)
	}
	if err := applyTarget(cfg, rest[0], false); err != nil {
		return Config{}, err
	}
	return *cfg, nil
}

func applyCommandTarget(cfg *Config, rest []string, action string) (Config, error) {
	if len(rest) < 2 {
		return Config{}, fmt.Errorf("%s requires target and input", action)
	}
	if err := applyTarget(cfg, rest[0], false); err != nil {
		return Config{}, err
	}
	cfg.Command = strings.Join(rest[1:], " ")
	return *cfg, nil
}

func applyTarget(cfg *Config, target string, partial bool) error {
	// Public targets are paths. Internally they map to session/pane/tab.
	parts := strings.Split(target, "/")
	if len(parts) > 3 {
		return fmt.Errorf("invalid target %q", target)
	}
	for _, part := range parts {
		if part == "" {
			return fmt.Errorf("invalid target %q", target)
		}
	}

	cfg.Session = parts[0]
	if len(parts) >= 2 {
		cfg.Pane = parts[1]
	} else if partial {
		cfg.Pane = ""
	}
	if len(parts) >= 3 {
		cfg.Tab = parts[2]
	} else if partial {
		cfg.Tab = ""
	}
	return nil
}
