package app

import (
	"errors"
	"flag"
	"fmt"
	"strings"
	"time"

	"ptymux/internal/server"
)

type Mode string

type Action string

const (
	ModeLocal  Mode = "local"
	ModeServer Mode = "server"
	ModeClient Mode = "client"
)

const (
	ActionRun      Action = "run"
	ActionHelp     Action = "help"
	ActionDaemon   Action = "daemon"
	ActionList     Action = "list"
	ActionKill     Action = "kill"
	ActionStop     Action = "stop"
	ActionIdle     Action = "idle"
	ActionSend     Action = "send"
	ActionText     Action = "text"
	ActionCommand  Action = "command"
	ActionKeys     Action = "keys"
	ActionCtrlC    Action = "ctrl-c"
	ActionRead     Action = "read"
	ActionFollow   Action = "follow"
	ActionCreate   Action = "create"
	ActionClose    Action = "close"
	ActionRegister Action = "register"
	ActionRotate   Action = "rotate"
	ActionRevoke   Action = "revoke"
)

type Config struct {
	Mode      Mode
	Action    Action
	Session   string
	Pane      string
	Tab       string
	Command   string
	Socket    string
	Follow    bool
	Wait      time.Duration
	ReadCount int

	Alias        string
	URL          string
	Token        string
	TokenFile    string
	ClientName   string
	Password     string
	PasswordFile string

	Listen                  string
	ClientRegistry          string
	Shell                   string
	ShutdownTimeout         time.Duration
	MaxPreAuthConnections   int
	MaxConnections          int
	MaxConnectionsPerClient int
	MaxTargetsPerClient     int
	AuthRate                float64
	AuthBurst               int
}

func Parse(args []string) (Config, error) {
	if len(args) > 0 {
		switch args[0] {
		case "local":
			return ParseLocal(args[1:])
		case "server":
			return ParseServer(args[1:])
		case "client":
			return ParseClient(args[1:])
		}
	}
	return ParseLocal(args)
}

func ParseLocal(args []string) (Config, error) {
	if len(args) > 0 && args[0] == "local" {
		args = args[1:]
	}
	cfg, err := parseLocal(args)
	cfg.Mode = ModeLocal
	return validateParsedConfig(cfg, err)
}

func ParseClient(args []string) (Config, error) {
	cfg, err := parseRemoteClient(args)
	return validateParsedConfig(cfg, err)
}

func ParseServer(args []string) (Config, error) {
	cfg, err := parseServer(args)
	return validateParsedConfig(cfg, err)
}

func validateParsedConfig(cfg Config, err error) (Config, error) {
	if err != nil || cfg.Action == ActionHelp {
		return cfg, err
	}
	if err := validateConfig(cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func parseLocal(args []string) (Config, error) {
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

func parseServer(args []string) (Config, error) {
	cfg := Config{
		Mode:                    ModeServer,
		Listen:                  "0.0.0.0:8443",
		Shell:                   "/bin/bash",
		ShutdownTimeout:         10 * time.Second,
		MaxPreAuthConnections:   256,
		MaxConnections:          256,
		MaxConnectionsPerClient: 16,
		MaxTargetsPerClient:     64,
		AuthRate:                5,
		AuthBurst:               20,
	}
	if hasHelpArg(args) {
		cfg.Action = ActionHelp
		return cfg, nil
	}
	fs := flag.NewFlagSet("server", flag.ContinueOnError)
	fs.StringVar(&cfg.Listen, "listen", cfg.Listen, "HTTP listen address")
	fs.StringVar(&cfg.TokenFile, "token-file", "", "global server token file")
	fs.StringVar(&cfg.ClientRegistry, "client-registry", "", "persistent client registry path")
	fs.StringVar(&cfg.Shell, "shell", cfg.Shell, "target shell")
	fs.DurationVar(&cfg.ShutdownTimeout, "shutdown-timeout", cfg.ShutdownTimeout, "graceful shutdown timeout")
	fs.IntVar(&cfg.MaxPreAuthConnections, "max-pre-auth-connections", cfg.MaxPreAuthConnections, "maximum accepted TCP connections awaiting authentication")
	fs.IntVar(&cfg.MaxConnections, "max-connections", cfg.MaxConnections, "maximum pending and active authenticated WebSocket connections")
	fs.IntVar(&cfg.MaxConnectionsPerClient, "max-connections-per-client", cfg.MaxConnectionsPerClient, "maximum pending and active authenticated WebSocket connections per client")
	fs.IntVar(&cfg.MaxTargetsPerClient, "max-targets-per-client", cfg.MaxTargetsPerClient, "maximum targets per client")
	fs.Float64Var(&cfg.AuthRate, "auth-rate", cfg.AuthRate, "authentication and registration tokens added per second")
	fs.IntVar(&cfg.AuthBurst, "auth-burst", cfg.AuthBurst, "authentication and registration burst size")
	if err := fs.Parse(args); err != nil {
		return Config{}, err
	}
	if len(fs.Args()) != 0 {
		return Config{}, errors.New("server does not accept positional arguments")
	}
	if err := applyServerPathDefaults(&cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func applyServerPathDefaults(cfg *Config) error {
	if cfg.TokenFile != "" && cfg.ClientRegistry != "" {
		return nil
	}
	paths, err := defaultServerPaths()
	if err != nil {
		return err
	}
	if cfg.TokenFile == "" {
		cfg.TokenFile = paths.TokenFile
	}
	if cfg.ClientRegistry == "" {
		cfg.ClientRegistry = paths.ClientRegistry
	}
	return nil
}

func parseRemoteClient(args []string) (Config, error) {
	cfg := Config{
		Mode:    ModeClient,
		Session: "default",
		Pane:    "default",
		Tab:     "default",
	}
	if len(args) == 0 || hasHelpArg(args) || args[0] == "help" {
		cfg.Action = ActionHelp
		return cfg, nil
	}

	remaining, err := extractRemoteOptions(&cfg, args)
	if err != nil {
		return Config{}, err
	}
	if len(remaining) == 0 {
		return Config{}, errors.New("client requires an operation")
	}

	operationIndex := 0
	if !isRemoteOperation(remaining[0]) {
		cfg.Alias = remaining[0]
		operationIndex = 1
	}
	if operationIndex >= len(remaining) {
		return Config{}, errors.New("client alias requires an operation")
	}
	operation := remaining[operationIndex]
	operationArgs := remaining[operationIndex+1:]
	if !isRemoteOperation(operation) {
		return Config{}, fmt.Errorf("unknown client operation %q", operation)
	}
	if hasHelpArg(operationArgs) {
		cfg.Action = ActionHelp
		return cfg, nil
	}

	switch operation {
	case "register":
		cfg.Action = ActionRegister
		if len(operationArgs) != 0 {
			return Config{}, errors.New("register does not accept positional arguments")
		}
		return cfg, nil
	case "rotate":
		cfg.Action = ActionRotate
		if len(operationArgs) != 0 {
			return Config{}, errors.New("rotate does not accept positional arguments")
		}
		return cfg, nil
	case "revoke":
		cfg.Action = ActionRevoke
		if len(operationArgs) != 0 {
			return Config{}, errors.New("revoke does not accept positional arguments")
		}
		return cfg, nil
	case "create":
		cfg.Action = ActionCreate
		return applyTargetOnly(&cfg, operationArgs, operation)
	case "close":
		cfg.Action = ActionClose
		return applyTargetOnly(&cfg, operationArgs, operation)
	case "list":
		cfg.Action = ActionList
		if len(operationArgs) > 1 {
			return Config{}, errors.New("list accepts at most one target")
		}
		if len(operationArgs) == 0 {
			cfg.Session, cfg.Pane, cfg.Tab = "", "", ""
			return cfg, nil
		}
		if err := applyTarget(&cfg, operationArgs[0], true); err != nil {
			return Config{}, err
		}
		return cfg, nil
	case "send":
		cfg.Action = ActionSend
		return applyCommandTarget(&cfg, operationArgs, operation)
	case "text":
		cfg.Action = ActionText
		return applyCommandTarget(&cfg, operationArgs, operation)
	case "keys":
		cfg.Action = ActionKeys
		return applyCommandTarget(&cfg, operationArgs, operation)
	case "read":
		cfg.Action = ActionRead
		fs := flag.NewFlagSet("client read", flag.ContinueOnError)
		fs.IntVar(&cfg.ReadCount, "n", 0, "number of recent terminal history lines")
		if err := fs.Parse(operationArgs); err != nil {
			return Config{}, err
		}
		if cfg.ReadCount < 0 {
			return Config{}, errors.New("read count must not be negative")
		}
		return applyTargetOnly(&cfg, fs.Args(), operation)
	case "follow":
		cfg.Action = ActionFollow
		cfg.Follow = true
		return applyTargetOnly(&cfg, operationArgs, operation)
	default:
		return Config{}, fmt.Errorf("unknown client operation %q", operation)
	}
}

func isRemoteOperation(value string) bool {
	switch value {
	case "register", "rotate", "revoke", "create", "list", "send", "text", "keys", "read", "follow", "close":
		return true
	default:
		return false
	}
}

func extractRemoteOptions(cfg *Config, args []string) ([]string, error) {
	remaining := make([]string, 0, len(args))
	operation := ""
	targetConsumed := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			remaining = append(remaining, args[i+1:]...)
			break
		}
		if operation != "" && remoteOperationHasPayload(operation) && targetConsumed {
			remaining = append(remaining, arg)
			continue
		}

		name, value, hasValue := strings.Cut(arg, "=")
		var target *string
		switch name {
		case "--url":
			target = &cfg.URL
		case "--token":
			target = &cfg.Token
		case "--token-file":
			target = &cfg.TokenFile
		case "--name":
			target = &cfg.ClientName
		case "--password":
			target = &cfg.Password
		case "--password-file":
			target = &cfg.PasswordFile
		default:
			remaining = append(remaining, arg)
			if operation == "" && isRemoteOperation(arg) {
				operation = arg
			} else if operation != "" && remoteOperationHasPayload(operation) {
				targetConsumed = true
			}
			continue
		}
		if !hasValue {
			i++
			if i >= len(args) {
				return nil, fmt.Errorf("%s requires a value", name)
			}
			value = args[i]
		}
		if value == "" {
			return nil, fmt.Errorf("%s requires a non-empty value", name)
		}
		*target = value
	}
	if cfg.Token != "" && cfg.TokenFile != "" {
		return nil, errors.New("--token and --token-file cannot be used together")
	}
	if cfg.Password != "" && cfg.PasswordFile != "" {
		return nil, errors.New("--password and --password-file cannot be used together")
	}
	return remaining, nil
}

func remoteOperationHasPayload(operation string) bool {
	switch operation {
	case "send", "text", "keys":
		return true
	default:
		return false
	}
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
	fs.IntVar(&cfg.ReadCount, "n", 0, "number of recent terminal history lines")
	if err := fs.Parse(args); err != nil {
		return Config{}, err
	}
	if cfg.ReadCount < 0 {
		return Config{}, errors.New("read count must not be negative")
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

func validateConfig(cfg Config) error {
	if cfg.Mode == ModeServer {
		if cfg.MaxPreAuthConnections < 0 {
			return errors.New("maximum pre-auth connections must not be negative")
		}
		if cfg.MaxConnections < 0 || cfg.MaxConnectionsPerClient < 0 || cfg.MaxTargetsPerClient < 0 {
			return errors.New("server limits must not be negative")
		}
		return nil
	}
	switch cfg.Action {
	case ActionRegister, ActionRotate, ActionRevoke, ActionDaemon, ActionStop, ActionHelp:
		return nil
	case ActionRun, ActionList, ActionKill, ActionIdle, ActionSend, ActionText, ActionCommand, ActionKeys, ActionCtrlC, ActionRead, ActionFollow, ActionCreate, ActionClose:
		return server.ValidateRequest(serverRequestFromConfig(cfg))
	default:
		return nil
	}
}

func LocalHelpText() string {
	return strings.TrimLeft(`
ptymux - persistent local command-line PTY targets

Usage:
  ptymux [--socket PATH] <target> <command>
  ptymux local [--socket PATH] <target> <command>
  ptymux [local] idle [-t DURATION] <target> <input>
  ptymux [local] send [-f | -t DURATION] <target> <input>
  ptymux [local] text <target> <text>
  ptymux [local] command [-f | -t DURATION] <target> <keys>
  ptymux [local] keys [-f | -t DURATION] <target> <keys>
  ptymux [local] ctrl-c <target>
  ptymux [local] read [-n N] <target>
  ptymux [local] follow <target>
  ptymux [local] list [target]
  ptymux [local] kill [target]
  ptymux [local] stop

Targets:
  work             -> work/default/default
  work/main        -> work/main/default
  work/main/build  -> work/main/build
  Each component: valid UTF-8, at most 64 bytes, no slash/control characters
  Command/input text: at most 128 KiB; read -n N: 0 through 4096

Terminal output:
  read returns the current terminal screen with ANSI styling
  read -n N returns the most recent N ANSI terminal history lines
  follow streams future raw PTY output in real time

Config:
  ~/.ptymux/config (preferred)
  ~/.ptymux/config.json (legacy fallback)
  shell defaults to /bin/sh
  auto_release.target_idle_timeout defaults to 8h
  auto_release.daemon_idle_timeout defaults to 30m
`, "\n")
}

func ClientHelpText() string {
	return strings.TrimLeft(`
ptymux-client - remote ptymux client

Registration:
  ptymux-client register --url http://HOST [--token-file PATH] --name NAME

Operations:
  ptymux-client [connection options] create <target>
  ptymux-client [connection options] list [target]
  ptymux-client [connection options] send <target> <input>
  ptymux-client [connection options] text <target> <text>
  ptymux-client [connection options] keys <target> <keys>
  ptymux-client [connection options] read [-n N] <target>
  ptymux-client [connection options] follow <target>
  ptymux-client [connection options] close <target>
  ptymux-client [connection options] rotate
  ptymux-client [connection options] revoke
  ptymux-client ALIAS <operation> ...

Connection options:
  --url URL --token VALUE|--token-file PATH --name NAME
  --password VALUE|--password-file PATH
  Default token: ~/.ptymux/client/server.token
  Default password: ~/.ptymux/client/<client-name>.password
  Aliases: ~/.ptymux/client/config

Targets:
  work             -> work/default/default
  work/main        -> work/main/default
  work/main/build  -> work/main/build
  Each component: valid UTF-8, at most 64 bytes, no slash/control characters
  Command/input text: at most 128 KiB; read -n N: 0 through 4096

Transport security:
  remote traffic uses unencrypted HTTP/WS and must stay on a trusted network
`, "\n")
}

func ServerHelpText() string {
	return strings.TrimLeft(`
ptymux-server - remote ptymux HTTP/WS server

Usage:
  ptymux-server [--listen ADDRESS] [--token-file PATH] \
    [--client-registry PATH] [--shell PATH]

Defaults:
  listen: 0.0.0.0:8443
  token: ~/.ptymux/server/token
  client registry: ~/.ptymux/server/clients.json

Optional limits:
  --max-pre-auth-connections N
  --max-connections N
  --max-connections-per-client N
  --max-targets-per-client N
  --auth-rate N
  --auth-burst N

Transport security:
  traffic uses unencrypted HTTP/WS and must stay on a trusted network
`, "\n")
}

func HelpText() string {
	return LocalHelpText() + "\n" + ServerHelpText() + "\n" + ClientHelpText()
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
