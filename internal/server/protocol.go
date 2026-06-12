package server

type Request struct {
	Action  string `json:"action"`
	Session string `json:"session,omitempty"`
	Pane    string `json:"pane,omitempty"`
	Tab     string `json:"tab,omitempty"`
	Command string `json:"command,omitempty"`
}

type Response struct {
	Output   string   `json:"output,omitempty"`
	ExitCode int      `json:"exit_code,omitempty"`
	Snapshot Snapshot `json:"snapshot,omitempty"`
	Error    string   `json:"error,omitempty"`
}

type RunResult struct {
	Output   string
	ExitCode int
}
