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
)

// execResponse type constants.
const (
	responseTypeResult  = "result"
	responseTypeTTY     = "tty"
	responseTypePong    = "pong"
	responseTypeVersion = "version"
	responseTypeError   = "error"
	responseTypeStdout  = "stdout"
	responseTypeStderr  = "stderr"
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
	Type    string            `json:"type"`              // "exec", "exec-tty", "ping"
	Command string            `json:"command,omitempty"` // shell command for exec/exec-tty
	Token   string            `json:"token,omitempty"`   // auth token
	Timeout int               `json:"timeout,omitempty"` // timeout in seconds
	User    string            `json:"user,omitempty"`    // run as this user
	Env     map[string]string `json:"env,omitempty"`     // extra environment variables
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
