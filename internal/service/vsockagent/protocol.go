package vsockagent

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
)

// execRequest type constants.
const (
	requestTypeExec    = "exec"
	requestTypeExecTTY = "exec-tty"
	requestTypePing    = "ping"
	requestTypeVersion = "version"
	requestTypeResize  = "resize"
)

// execResponse type constants.
const (
	responseTypeResult   = "result"
	responseTypeTTY      = "tty"
	responseTypePong     = "pong"
	responseTypeVersion  = "version"
	responseTypeError    = "error"
	responseTypeStdout   = "stdout"
	responseTypeStderr   = "stderr"
	responseTypeRemoteVM = "remote_vm"
)

// execRequest type constants (additional).
const (
	requestTypeRemoteVM = "remote_vm"
)

// File-transfer request/response types.
const (
	requestTypeFileTransfer = "file-transfer"
	responseTypeFTReady     = "ft-ready"
	ftBufferSize            = 262144
)

// execRequest is a JSON frame received from the host agent.
type execRequest struct {
	ID      string            `json:"id"`
	Type    string            `json:"type"`              // "exec", "exec-tty", "ping", "resize"
	Command string            `json:"command,omitempty"` // shell command for exec/exec-tty
	Token   string            `json:"token,omitempty"`   // auth token
	Timeout int               `json:"timeout,omitempty"` // timeout in seconds
	User    string            `json:"user,omitempty"`    // run as this user
	Env     map[string]string `json:"env,omitempty"`     // extra environment variables
	NoSync  bool              `json:"no_sync,omitempty"` // skip sync() after command
	Rows    int               `json:"rows,omitempty"`    // terminal rows (exec-tty / resize)
	Cols    int               `json:"cols,omitempty"`    // terminal columns
}

// execResponse is a JSON frame sent back to the host agent.
type execResponse struct {
	ID         string `json:"id,omitempty"`
	Type       string `json:"type"`             // "result", "tty", "pong", "error", "stdout", "stderr"
	Status     int    `json:"status,omitempty"` // exit code
	Data       string `json:"data,omitempty"`   // streaming stdout/stderr chunk
	Stdout     string `json:"stdout,omitempty"`
	Stderr     string `json:"stderr,omitempty"`
	DurationMs int    `json:"duration_ms,omitempty"`
	Error      string `json:"error,omitempty"`
}

// RemoteVMRequest is the JSON payload for a "remote_vm" request from the guest
// to the host. Sent via local socket from the CLI subcommand to the daemon,
// then relayed through the vsock connection to the host.
type RemoteVMRequest struct {
	Destination string `json:"destination"`
	Command     string `json:"command"`
	User        string `json:"user,omitempty"`
	Timeout     int    `json:"timeout,omitempty"`
}

// RemoteVMResponse is the JSON response for a "remote_vm" operation, sent by
// the host back to the guest agent through the vsock connection.
type RemoteVMResponse struct {
	Type   string `json:"type"`
	Status int    `json:"status,omitempty"`
	Error  string `json:"error,omitempty"`
}

// readFrame reads one newline-delimited JSON request from r.
func readFrame(r io.Reader) (*execRequest, error) {
	br, ok := r.(*bufio.Reader)
	if !ok {
		br = bufio.NewReader(r)
	}
	line, err := br.ReadBytes('\n')
	if err != nil {
		return nil, fmt.Errorf("read frame: %w", err)
	}
	line = trimBOM(line)
	req := &execRequest{}
	if err := json.Unmarshal(line, req); err != nil {
		return nil, fmt.Errorf("parse frame: %w", err)
	}
	return req, nil
}

// writeFrame writes resp as newline-delimited JSON to w.
func writeFrame(w io.Writer, resp *execResponse) error {
	data, err := json.Marshal(resp)
	if err != nil {
		return fmt.Errorf("marshal response: %w", err)
	}
	data = append(data, '\n')
	if _, err := w.Write(data); err != nil {
		return fmt.Errorf("write frame: %w", err)
	}
	return nil
}

// trimBOM removes a UTF-8 BOM if present, for robustness with Windows-origin data.
func trimBOM(line []byte) []byte {
	if len(line) >= 3 && line[0] == 0xef && line[1] == 0xbb && line[2] == 0xbf {
		return line[3:]
	}
	return line
}
