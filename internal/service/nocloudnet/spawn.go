package nocloudnet

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"mvmctl/internal/lib/system"
	"mvmctl/pkg/errs"
)

// SpawnResult describes a listener acknowledged by the newly spawned child.
type SpawnResult struct {
	URL  string
	Port int
	PID  int
}

// Spawn starts one batch listener and waits for its private readiness acknowledgement.
// An occupied gateway port is an error; existing listeners are never adopted or signalled.
func Spawn(ctx context.Context, cfg Config) (*SpawnResult, error) {
	if ctx == nil {
		return nil, errs.New(errs.CodeValidationFailed, "NoCloud spawn context is required")
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		return nil, errs.Wrap(errs.CodeCloudInitNetModeFailed, err)
	}
	defer reader.Close()
	defer writer.Close()
	dirFlag, directory := "--cloud-init-dir", cfg.CloudInitDir
	if cfg.BaseDir != "" {
		dirFlag, directory = "--base-dir", cfg.BaseDir
	}
	cmd, err := system.SpawnService(nil, system.SpawnConfig{
		Name: "nocloudnet", ExtraFiles: []*os.File{writer},
		Args: []string{"serve", dirFlag, directory, "--network-id", cfg.NetworkID,
			"--log-file", cfg.LogFile, "--kill-after", cfg.KillAfter.String(), "--notify-ready"},
	})
	if err != nil {
		return nil, errs.Wrap(errs.CodeCloudInitNetModeFailed, err)
	}
	if err := writer.Close(); err != nil {
		return nil, errors.Join(err, cmd.Process.Kill(), cmd.Wait())
	}
	deadline := time.Now().Add(30 * time.Second)
	if earlier, ok := ctx.Deadline(); ok && earlier.Before(deadline) {
		deadline = earlier
	}
	if err := reader.SetReadDeadline(deadline); err != nil {
		return nil, errors.Join(err, cmd.Process.Kill(), cmd.Wait())
	}
	stop := context.AfterFunc(ctx, func() {
		// Interrupt startup only; the child owns its lifetime after acknowledgement.
		_ = reader.SetReadDeadline(time.Now())
	})
	defer stop()
	var startup startupResult
	decodeErr := json.NewDecoder(io.LimitReader(reader, 4096)).Decode(&startup)
	if decodeErr == nil && startup.Error == "" && startup.Endpoint != nil {
		decodeErr = startup.Endpoint.Validate(cfg.NetworkID)
		if decodeErr == nil && ctx.Err() == nil {
			// Reap this exact child when it exits; persisted numeric PIDs never authorize cleanup.
			go func() { _ = cmd.Wait() }()
			return &SpawnResult{URL: fmt.Sprintf("http://%s:%d/", startup.Endpoint.Gateway, startup.Endpoint.Port),
				Port: int(startup.Endpoint.Port), PID: cmd.Process.Pid}, nil
		}
	}
	if startup.Error == "" {
		startup.Error = "NoCloud child did not acknowledge a ready listener"
	}
	// The child is still ours and unreaped, so this cannot target a reused or unrelated PID.
	killErr := cmd.Process.Kill()
	if errors.Is(killErr, os.ErrProcessDone) {
		killErr = nil
	}
	waitErr := cmd.Wait()
	return nil, errs.WrapMsg(errs.CodeCloudInitNetModeFailed, startup.Error,
		errors.Join(decodeErr, ctx.Err(), killErr, waitErr))
}
