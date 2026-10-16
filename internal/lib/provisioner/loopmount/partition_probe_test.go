package loopmount_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/lib/provisioner/loopmount"
	"mvmctl/internal/lib/system"
	"mvmctl/internal/testutil"
	"mvmctl/pkg/errs"
)

// partitionProbeRunner adds command-specific results and fixture-only copy effects to FakeRunner.
// Tests remain serial because DefaultRunner is package-global.
type partitionProbeRunner struct {
	testutil.FakeRunner
	sfdiskResult *system.RunResult
	sfdiskErr    error
	partedResult *system.RunResult
	partedErr    error
	fsType       string
	cpResult     *system.RunResult
	cpErr        error
	cpPartial    []byte
	cancelProbe  string
	cancel       context.CancelFunc
}

func (r *partitionProbeRunner) Run(
	ctx context.Context, args []string, opts system.RunCmdOpts,
) (*system.RunResult, error) {
	result, err := &system.RunResult{}, error(nil)
	switch args[0] {
	case "blkid":
		result.Stdout = r.fsType
	case "sfdisk":
		result, err = r.sfdiskResult, r.sfdiskErr
	case "parted":
		result, err = r.partedResult, r.partedErr
	case "cp":
		output := args[len(args)-1]
		if writeErr := os.WriteFile(output, r.cpPartial, 0o440); writeErr != nil {
			return &system.RunResult{ExitCode: -1}, writeErr
		}
		if chmodErr := os.Chmod(output, 0o440); chmodErr != nil {
			return &system.RunResult{ExitCode: -1}, chmodErr
		}
		result, err = r.cpResult, r.cpErr
	case "dd":
		var input, output string
		var skip, count int64
		for _, arg := range args[1:] {
			key, value, _ := strings.Cut(arg, "=")
			switch key {
			case "if":
				input = value
			case "of":
				output = value
			case "skip", "count":
				n, parseErr := strconv.ParseInt(value, 10, 64)
				if parseErr != nil {
					return &system.RunResult{ExitCode: -1}, parseErr
				}
				if key == "skip" {
					skip = n
				} else {
					count = n
				}
			}
		}
		data, readErr := os.ReadFile(input)
		if readErr != nil {
			return &system.RunResult{ExitCode: -1}, readErr
		}
		end := int64(len(data))
		if count > 0 {
			end = skip + count
		}
		if skip < 0 || end < skip || end > int64(len(data)) {
			return &system.RunResult{ExitCode: -1}, fmt.Errorf("fixture dd range %d:%d is invalid", skip, end)
		}
		err = os.WriteFile(output, data[skip:end], 0o600)
	default:
		err = fmt.Errorf("unexpected fixture command %q", args[0])
		result.ExitCode = -1
	}
	if args[0] == r.cancelProbe {
		r.cancel()
	}
	r.StubRunResult, r.StubRunErr = result, err
	return r.FakeRunner.Run(ctx, args, opts)
}

// installPartitionProbeRunner restores the global runner even when an assertion fails.
func installPartitionProbeRunner(t *testing.T, runner *partitionProbeRunner) {
	t.Helper()
	previous := system.DefaultRunner
	system.DefaultRunner = runner
	t.Cleanup(func() { system.DefaultRunner = previous })
}

func TestExtractPartitionProbeFailureFallsBack(t *testing.T) {
	// Rationale: A failed sfdisk probe must not authorize moving a partitioned disk as-is.
	tests := map[string]struct {
		result *system.RunResult
		err    error
	}{
		"unavailable sfdisk": {
			result: &system.RunResult{ExitCode: -1}, err: errors.New("fixture sfdisk unavailable"),
		},
		"native sfdisk failure": {result: &system.RunResult{ExitCode: 1, Stderr: "fixture read failure"}},
		"invalid sfdisk output": {result: &system.RunResult{Stdout: "not JSON"}},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			rawPath, outputPath := filepath.Join(dir, "disk.raw"), filepath.Join(dir, "root.img")
			rootBytes := bytes.Repeat([]byte("R"), 512)
			diskBytes := append(bytes.Repeat([]byte("B"), 1024), rootBytes...)
			diskBytes = append(diskBytes, bytes.Repeat([]byte("T"), 512)...)
			require.NoError(t, os.WriteFile(rawPath, diskBytes, 0o600))
			runner := &partitionProbeRunner{
				sfdiskResult: tc.result, sfdiskErr: tc.err,
				partedResult: &system.RunResult{Stdout: "BYT;\n/disk:2048B:file:512:512:gpt:;\n" +
					"1:512B:1023B:512B:fat32:boot:;\n2:1024B:1535B:512B:ext4:root:;\n"},
			}
			installPartitionProbeRunner(t, runner)
			backend := loopmount.NewLoopMountBackend(context.Background(), rawPath, "", dir)
			gotPath, err := backend.ExtractPartition(context.Background(), rawPath, outputPath, 0, nil)
			require.NoError(t, err)
			assert.Equal(t, outputPath, gotPath)
			gotBytes, err := os.ReadFile(gotPath)
			require.NoError(t, err)
			// INPUT: parted identifies only sector two as the ext4 root; boot/trailer bytes must be absent.
			if diff := cmp.Diff(rootBytes, gotBytes); diff != "" {
				t.Errorf("extracted root bytes (-want +got):\n%s", diff)
			}
			preserved, err := os.ReadFile(rawPath)
			require.NoError(t, err)
			if diff := cmp.Diff(diskBytes, preserved); diff != "" {
				t.Errorf("source disk bytes (-want +got):\n%s", diff)
			}
		})
	}
}

func TestExtractPartitionFailedProbesPreserveFiles(t *testing.T) {
	// Rationale: Unavailable/native-failing probes and cancellation provide no whole-disk authority.
	sfdiskErr, partedErr := errors.New("fixture sfdisk unavailable"), errors.New("fixture parted unavailable")
	tests := map[string]struct {
		runner      *partitionProbeRunner
		cancelFirst bool
		wantErrors  []error
		wantText    []string
	}{
		"both unavailable": {
			runner: &partitionProbeRunner{
				sfdiskResult: &system.RunResult{ExitCode: -1}, sfdiskErr: sfdiskErr,
				partedResult: &system.RunResult{ExitCode: -1}, partedErr: partedErr,
			},
			wantErrors: []error{sfdiskErr, partedErr},
		},
		"both native failures": {
			runner: &partitionProbeRunner{
				sfdiskResult: &system.RunResult{ExitCode: 2, Stderr: "sfdisk native diagnostic"},
				partedResult: &system.RunResult{ExitCode: 1, Stderr: "parted native diagnostic"},
			},
			wantText: []string{"sfdisk native diagnostic", "parted native diagnostic"},
		},
		"truncated parted header": {
			runner: &partitionProbeRunner{
				sfdiskResult: &system.RunResult{ExitCode: -1}, sfdiskErr: sfdiskErr,
				partedResult: &system.RunResult{Stdout: "BYT;"},
			},
			wantErrors: []error{sfdiskErr}, wantText: []string{"invalid parted partition table"},
		},
		"already canceled": {cancelFirst: true, wantErrors: []error{context.Canceled}},
		"canceled during sfdisk": {
			runner: &partitionProbeRunner{
				sfdiskResult: &system.RunResult{ExitCode: -1}, sfdiskErr: sfdiskErr, cancelProbe: "sfdisk",
			},
			wantErrors: []error{context.Canceled},
		},
		"canceled during parted": {
			runner: &partitionProbeRunner{
				sfdiskResult: &system.RunResult{ExitCode: -1}, sfdiskErr: sfdiskErr,
				partedResult: &system.RunResult{ExitCode: -1}, partedErr: partedErr, cancelProbe: "parted",
			},
			wantErrors: []error{context.Canceled},
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.runner == nil {
				tc.runner = &partitionProbeRunner{}
			}
			tc.runner.cancel = cancel
			if tc.cancelFirst {
				cancel()
			}
			installPartitionProbeRunner(t, tc.runner)
			dir := t.TempDir()
			rawPath, outputPath := filepath.Join(dir, "disk.raw"), filepath.Join(dir, "root.img")
			diskBytes, outputBytes := []byte("source disk must remain"), []byte("existing output must remain")
			require.NoError(t, os.WriteFile(rawPath, diskBytes, 0o600))
			require.NoError(t, os.WriteFile(outputPath, outputBytes, 0o600))
			backend := loopmount.NewLoopMountBackend(ctx, rawPath, "", dir)
			_, err := backend.ExtractPartition(ctx, rawPath, outputPath, 0, nil)
			require.Error(t, err)
			for _, wantErr := range tc.wantErrors {
				assert.ErrorIs(t, err, wantErr)
			}
			if len(tc.wantText) > 0 {
				// DomainError.Error exposes the public message; diagnostics belong to its retained cause.
				cause := errors.Unwrap(err)
				require.NotNil(t, cause)
				for _, text := range tc.wantText {
					assert.Contains(t, cause.Error(), text)
				}
			}
			for path, want := range map[string][]byte{rawPath: diskBytes, outputPath: outputBytes} {
				got, readErr := os.ReadFile(path)
				require.NoError(t, readErr)
				if diff := cmp.Diff(want, got); diff != "" {
					t.Errorf("preserved %s bytes (-want +got):\n%s", filepath.Base(path), diff)
				}
			}
			for _, call := range tc.runner.Calls {
				assert.NotEqual(t, "dd", call.Args[0], "failed/canceled probes must not start extraction")
				if tc.cancelFirst {
					t.Error("already-canceled extraction must not invoke any probes")
				}
				if tc.runner.cancelProbe == "sfdisk" {
					assert.NotEqual(t, "parted", call.Args[0], "cancellation must stop probe fallback")
				}
			}
		})
	}
}

func TestExtractPartitionSuccessfulSfdisk(t *testing.T) {
	// Rationale: Probe error handling must preserve successful table and empty-table behavior.
	rootBytes := bytes.Repeat([]byte("R"), 512)
	diskBytes := append(bytes.Repeat([]byte("B"), 512), rootBytes...)
	tests := map[string]struct {
		stdout string
		want   []byte
	}{
		"partition table": {
			stdout: `{"partitiontable":{"partitions":[{"start":1,"size":1,"type":"83","node":"disk1"}]}}`,
			want:   rootBytes,
		},
		"successful empty table": {
			stdout: `{"partitiontable":{"partitions":[]}}`, want: diskBytes,
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			rawPath, outputPath := filepath.Join(dir, "disk.raw"), filepath.Join(dir, "root.img")
			require.NoError(t, os.WriteFile(rawPath, diskBytes, 0o600))
			installPartitionProbeRunner(t, &partitionProbeRunner{
				sfdiskResult: &system.RunResult{Stdout: tc.stdout},
				partedResult: &system.RunResult{ExitCode: -1}, partedErr: errors.New("unexpected parted fallback"),
			})
			backend := loopmount.NewLoopMountBackend(context.Background(), rawPath, "", dir)
			gotPath, err := backend.ExtractPartition(context.Background(), rawPath, outputPath, 0, nil)
			require.NoError(t, err)
			assert.Equal(t, outputPath, gotPath)
			got, err := os.ReadFile(gotPath)
			require.NoError(t, err)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("successful sfdisk extraction bytes (-want +got):\n%s", diff)
			}
		})
	}
}

func TestCopyRootPartitionFailedCopyPreservesFiles(t *testing.T) {
	// Rationale: Retrying dd against a read-only partial copy masks cp's original failure.
	diagnostic := "cp: original copy failure: " + strings.Repeat("retained diagnostic ", 8) + "end"
	tests := map[string]struct {
		result   *system.RunResult
		err      error
		wantText string
	}{
		"native copy failure": {
			result:   &system.RunResult{ExitCode: 1, Stderr: diagnostic + "\n"},
			wantText: "exit 1",
		},
		"runner failure despite successful exit status": {
			result: &system.RunResult{Stderr: diagnostic + "\n"}, err: context.Canceled,
			wantText: "cp failed",
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			rawPath, outputPath := filepath.Join(dir, "disk.raw"), filepath.Join(dir, "root.img")
			sourceBytes := []byte("independent VM export bytes must remain intact")
			partialBytes := sourceBytes[:12]
			require.NoError(t, os.WriteFile(rawPath, sourceBytes, 0o400))
			before, err := os.Stat(rawPath)
			require.NoError(t, err)
			runner := &partitionProbeRunner{
				fsType: "ext4", cpResult: tc.result, cpErr: tc.err, cpPartial: partialBytes,
			}
			installPartitionProbeRunner(t, runner)
			backend := loopmount.NewLoopMountBackend(context.Background(), rawPath, "ext4", dir)
			_, err = backend.CopyRootPartition(context.Background(), rawPath, outputPath, 0, nil)
			require.Error(t, err)
			var domainErr *errs.DomainError
			require.ErrorAs(t, err, &domainErr)
			assert.Equal(t, errs.CodeProcessError, domainErr.Code)
			assert.Contains(t, err.Error(), diagnostic)
			assert.Contains(t, err.Error(), tc.wantText)
			if tc.err != nil {
				assert.ErrorIs(t, err, tc.err)
			}
			// Read back the failed-copy evidence and the independent source, not only runner calls.
			for path, want := range map[string][]byte{rawPath: sourceBytes, outputPath: partialBytes} {
				got, readErr := os.ReadFile(path)
				require.NoError(t, readErr)
				if diff := cmp.Diff(want, got); diff != "" {
					t.Errorf("preserved %s bytes (-want +got):\n%s", filepath.Base(path), diff)
				}
			}
			after, err := os.Stat(rawPath)
			require.NoError(t, err)
			assert.True(t, os.SameFile(before, after), "failed extraction must retain the source inode")
			assert.Equal(t, before.Mode(), after.Mode(), "failed extraction must retain the source mode")
			partial, err := os.Stat(outputPath)
			require.NoError(t, err)
			assert.Equal(t, os.FileMode(0o440), partial.Mode().Perm(), "partial output must remain read-only")
			_, err = os.Stat(filepath.Join(dir, "root.ext4"))
			assert.True(t, os.IsNotExist(err), "failed copy must not publish the filesystem image")
			for _, call := range runner.Calls {
				assert.NotEqual(t, "dd", call.Args[0], "failed copy must not start a second copy")
			}
		})
	}
}
