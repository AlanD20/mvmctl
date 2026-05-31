// Package loopmount wire protocol — JSON stdin/stdout protocol for the
// _provision hidden subcommand. Matches Python's process.py exactly.
package loopmount

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
)

// WireInput matches Python's process.py input format exactly.
type WireInput struct {
	Image    string         `json:"image"`
	Action   string         `json:"action"`
	FsType   string         `json:"fs_type,omitempty"`
	Debug    bool           `json:"debug,omitempty"`
	TargetFS string         `json:"target_fs,omitempty"`
	Shell    string         `json:"shell,omitempty"`
	Ops      WireOperations `json:"operations,omitempty"`
}

// WireOperations holds the provisioning operations from JSON input.
type WireOperations struct {
	Files    []WireFileOp    `json:"files,omitempty"`
	CopyDirs []WireCopyDirOp `json:"copy_dirs,omitempty"`
	Commands []string        `json:"commands,omitempty"`
	Resize   *WireResizeOp   `json:"resize,omitempty"`
}

// WireFileOp describes a file to write (Data is base64-encoded in JSON).
type WireFileOp struct {
	Path string `json:"path"`
	Data string `json:"data"` // base64-encoded content
	Mode int    `json:"mode,omitempty"`
	UID  int    `json:"uid,omitempty"`
	GID  int    `json:"gid,omitempty"`
}

// WireCopyDirOp describes a directory to copy.
type WireCopyDirOp struct {
	Src  string `json:"src"`
	Dst  string `json:"dst"`
	Mode int    `json:"mode,omitempty"`
}

// WireResizeOp describes a filesystem resize.
type WireResizeOp struct {
	Action   string `json:"action"`
	Bytes    int64  `json:"bytes,omitempty"`
	Headroom int    `json:"headroom,omitempty"`
}

// WireOutput matches Python's process.py output format exactly.
type WireOutput struct {
	Status       string `json:"status"`
	Error        string `json:"error,omitempty"`
	Step         string `json:"step,omitempty"`
	FilesWritten int    `json:"files_written,omitempty"`
	CommandsRun  int    `json:"commands_run,omitempty"`
	OsType       string `json:"os_type,omitempty"`
	Note         string `json:"note,omitempty"`
	NewFSType    string `json:"new_fs_type,omitempty"`
	NewSizeBytes int64  `json:"new_size_bytes,omitempty"`
}

// ExecuteWireProtocol handles the full stdin/stdout wire protocol:
// parses raw JSON input, converts to service types, executes,
// converts result back to JSON output.
func ExecuteWireProtocol(ctx context.Context, rawInput []byte) ([]byte, error) {
	var input WireInput
	if err := json.Unmarshal(rawInput, &input); err != nil {
		return marshalWireError("Invalid JSON: "+err.Error(), "parse")
	}

	op := convertWireToOp(input)
	provisioner := NewProvisioner("/tmp/mvm-provision")
	results, err := provisioner.Execute(ctx, []Op{op})
	if err != nil {
		return marshalWireError(err.Error(), "execute")
	}
	if len(results) == 0 {
		return marshalWireError("no result returned", "execute")
	}

	return marshalWireResult(results[0])
}

// marshalWireResult converts a Result to JSON output bytes.
func marshalWireResult(r Result) ([]byte, error) {
	switch r.Status {
	case "ok":
		return json.Marshal(WireOutput{
			Status:       "ok",
			FilesWritten: r.FilesWritten,
			CommandsRun:  r.CommandsRun,
			OsType:       r.OSType,
			NewFSType:    r.NewFSType,
			NewSizeBytes: r.NewSizeBytes,
		})
	default:
		step := r.Step
		if step == "" {
			step = "unknown"
		}
		return json.Marshal(WireOutput{
			Status: "error",
			Error:  r.Error,
			Step:   step,
		})
	}
}

// marshalWireError creates JSON error output for wire protocol failures.
func marshalWireError(msg, step string) ([]byte, error) {
	return json.Marshal(WireOutput{
		Status: "error",
		Error:  msg,
		Step:   step,
	})
}

// convertWireToOp converts WireInput to the typed service Op.
func convertWireToOp(input WireInput) Op {
	op := Op{
		Image:    input.Image,
		Action:   input.Action,
		FsType:   input.FsType,
		TargetFS: input.TargetFS,
		Debug:    input.Debug,
		Shell:    input.Shell,
	}

	for _, f := range input.Ops.Files {
		var data []byte
		if f.Data != "" {
			data, _ = base64.StdEncoding.DecodeString(f.Data)
		}
		op.Files = append(op.Files, FileOp{
			Path: f.Path,
			Data: data,
			Mode: os.FileMode(f.Mode),
			UID:  f.UID,
			GID:  f.GID,
		})
	}

	for _, c := range input.Ops.CopyDirs {
		op.CopyDirs = append(op.CopyDirs, CopyDirOp{
			Src:  c.Src,
			Dst:  c.Dst,
			Mode: os.FileMode(c.Mode),
		})
	}

	if len(input.Ops.Commands) > 0 {
		op.Commands = input.Ops.Commands
	}

	if input.Ops.Resize != nil {
		op.Resize = &ResizeOp{
			Action:   input.Ops.Resize.Action,
			Bytes:    input.Ops.Resize.Bytes,
			Headroom: input.Ops.Resize.Headroom,
		}
	}

	return op
}
