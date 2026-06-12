package app

import (
	"errors"
	"flag"
	"fmt"
	"strings"
)

type Action string

const (
	ActionRun    Action = "run"
	ActionDaemon Action = "daemon"
	ActionList   Action = "list"
	ActionKill   Action = "kill"
	ActionStop   Action = "stop"
)

type Config struct {
	Action  Action
	Session string
	Pane    string
	Tab     string
	Command string
	Socket  string
}

func Parse(args []string) (Config, error) {
	cfg := Config{
		Action:  ActionRun,
		Session: "default",
		Pane:    "default",
		Tab:     "default",
	}

	if len(args) > 0 {
		switch args[0] {
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
		}
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
			if len(rest) != 1 {
				return Config{}, fmt.Errorf("%s does not accept positional arguments", cfg.Action)
			}
			return cfg, nil
		case "stop":
			cfg.Action = ActionStop
			if len(rest) != 1 {
				return Config{}, fmt.Errorf("%s does not accept positional arguments", cfg.Action)
			}
			return cfg, nil
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

	if len(rest) != 0 {
		return Config{}, fmt.Errorf("%s does not accept positional arguments", cfg.Action)
	}
	return cfg, nil
}

func applyTarget(cfg *Config, target string, partial bool) error {
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
