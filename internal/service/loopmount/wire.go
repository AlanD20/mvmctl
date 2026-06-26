// Package loopmount wire protocol — JSON stdin/stdout protocol for
// rootfs provisioning operations (wire, file ops, chroot commands, resize).
// The protocol is consumed by Run() (foreground) and Spawn() (subprocess).
package loopmount

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
)

// WireInput is the JSON input format for wire protocol operations.
type WireInput struct {
	Image    string         `json:"image"`
	Action   string         `json:"action"`
	FsType   string         `json:"fs_type,omitempty"`
	Debug    bool           `json:"debug,omitempty"`
	TargetFS string         `json:"target_fs,omitempty"`
	Shell    string         `json:"shell,omitempty"`
	Ops      WireOperations `json:"operations"`
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

// WireOutput is the JSON output format for wire protocol results.
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

// marshalWireResult converts a Result to JSON output bytes.
func marshalWireResult(r Result) []byte {
	var out WireOutput
	switch r.Status {
	case "ok":
		out = WireOutput{
			Status:       "ok",
			FilesWritten: r.FilesWritten,
			CommandsRun:  r.CommandsRun,
			OsType:       r.OSType,
			NewFSType:    r.NewFSType,
			NewSizeBytes: r.NewSizeBytes,
		}
	default:
		step := r.Step
		if step == "" {
			step = "unknown"
		}
		out = WireOutput{
			Status: "error",
			Error:  r.Error,
			Step:   step,
		}
	}
	b, _ := json.Marshal(out)
	return b
}

// marshalWireError creates JSON error output for wire protocol failures.
func marshalWireError(msg, step string) []byte {
	b, _ := json.Marshal(WireOutput{
		Status: "error",
		Error:  msg,
		Step:   step,
	})
	return b
}

// convertWireToOp converts WireInput to the typed service Op.
// Validates that required fields (Image, Action) are present.
func convertWireToOp(input WireInput) (Op, error) {
	if input.Image == "" {
		return Op{}, fmt.Errorf("image is required")
	}
	if input.Action == "" {
		return Op{}, fmt.Errorf("action is required")
	}

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
			var err error
			data, err = base64.StdEncoding.DecodeString(f.Data)
			if err != nil {
				slog.Warn("invalid base64 data in file operation, writing empty file",
					"path", f.Path, "error", err)
			}
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

	return op, nil
}
