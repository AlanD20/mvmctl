package logging

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/lib/vmexecwire"
	"mvmctl/pkg/errs"
)

const (
	testAuditRequestID     = "0123456789abcdef0123456789abcdef"
	testAuditSourceVMID    = "11111111111111111111111111111111"
	testAuditDestinationID = "22222222222222222222222222222222"
)

type fakeVMExecAuditWriter struct {
	mu         sync.Mutex
	records    [][]byte
	writeN     int
	writeErr   error
	closeErr   error
	closeCalls int
}

func (writer *fakeVMExecAuditWriter) WriteContext(ctx context.Context, value []byte) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	writer.mu.Lock()
	defer writer.mu.Unlock()
	writer.records = append(writer.records, append([]byte(nil), value...))
	if writer.writeN != 0 {
		return writer.writeN, writer.writeErr
	}
	return len(value), writer.writeErr
}

func (writer *fakeVMExecAuditWriter) Close() error {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	writer.closeCalls++
	return writer.closeErr
}

func validVMExecAuditRecord() VMExecAuditRecord {
	exitCode := 0
	return VMExecAuditRecord{
		RequestID:       testAuditRequestID,
		SourceVMID:      testAuditSourceVMID,
		DestinationVMID: testAuditDestinationID,
		TargetUser:      "builder",
		StartedAt:       time.Date(2026, time.September, 4, 10, 11, 12, 0, time.FixedZone("test", 2*60*60)),
		Duration:        1500 * time.Millisecond,
		Result:          VMExecAuditSucceeded,
		ExitCode:        &exitCode,
	}
}

func assertVMExecAuditError(t *testing.T, err error, code errs.Code, class errs.Class) {
	t.Helper()
	require.Error(t, err)
	domainErr := errs.AsDomainError(err)
	require.NotNil(t, domainErr)
	assert.Equal(t, code, domainErr.Code)
	assert.Equal(t, class, domainErr.Class)
	assert.Equal(t, "vsock", domainErr.Op)
}

// Rationale: the fixed projection is the data-minimization boundary that prevents commands, credentials, and causes
// from entering the audit log.
func TestVMExecAuditRecordWritesExactRedactedProjection(t *testing.T) {
	writer := &fakeVMExecAuditWriter{}
	sink := &VMExecAuditSink{writer: writer}

	require.NoError(t, sink.Record(t.Context(), validVMExecAuditRecord()))
	require.Len(t, writer.records, 1)
	assert.JSONEq(t, `{
		"schema_version": 1,
		"operation": "vm_exec",
		"request_id": "0123456789abcdef0123456789abcdef",
		"source_vm_id": "11111111111111111111111111111111",
		"destination_vm_id": "22222222222222222222222222222222",
		"target_user": "builder",
		"started_at": "2026-09-04T08:11:12Z",
		"duration_ms": 1500,
		"result": "succeeded",
		"exit_code": 0
	}`, string(writer.records[0]))
	assert.Equal(t, byte('\n'), writer.records[0][len(writer.records[0])-1])

	var fields map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(writer.records[0], &fields))
	assert.ElementsMatch(t, []string{
		"schema_version", "operation", "request_id", "source_vm_id", "destination_vm_id", "target_user",
		"started_at", "duration_ms", "result", "exit_code",
	}, mapKeys(fields))
}

func TestVMExecAuditRecordAcceptsClosedResultVariants(t *testing.T) {
	tests := map[string]struct {
		result   VMExecAuditResult
		exitCode *int
		duration time.Duration
		wantExit string
	}{
		"succeeded":          {result: VMExecAuditSucceeded, exitCode: intPointer(0), wantExit: "0"},
		"command failed min": {result: VMExecAuditCommandFailed, exitCode: intPointer(1), wantExit: "1"},
		"command failed max": {result: VMExecAuditCommandFailed, exitCode: intPointer(255), wantExit: "255"},
		"denied":             {result: VMExecAuditDenied, wantExit: "null"},
		"cancelled":          {result: VMExecAuditCancelled, wantExit: "null"},
		"timed out": {
			result: VMExecAuditTimedOut, duration: vmexecwire.MaxExecutionDuration, wantExit: "null",
		},
		"relay failed": {result: VMExecAuditRelayFailed, wantExit: "null"},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			writer := &fakeVMExecAuditWriter{}
			sink := &VMExecAuditSink{writer: writer}
			record := validVMExecAuditRecord()
			record.Result = test.result
			record.ExitCode = test.exitCode
			if test.duration != 0 {
				record.Duration = test.duration
			}
			require.NoError(t, sink.Record(t.Context(), record))
			require.Len(t, writer.records, 1)
			var fields map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(writer.records[0], &fields))
			assert.JSONEq(t, test.wantExit, string(fields["exit_code"]))
		})
	}
}

func TestVMExecAuditRecordRejectsInvalidValuesBeforeWrite(t *testing.T) {
	tests := map[string]func(*VMExecAuditRecord){
		"empty request ID":        func(record *VMExecAuditRecord) { record.RequestID = "" },
		"uppercase request ID":    func(record *VMExecAuditRecord) { record.RequestID = "A" + record.RequestID[1:] },
		"non-hex source VM ID":    func(record *VMExecAuditRecord) { record.SourceVMID = "g" + record.SourceVMID[1:] },
		"short destination ID":    func(record *VMExecAuditRecord) { record.DestinationVMID = "22" },
		"same source destination": func(record *VMExecAuditRecord) { record.DestinationVMID = record.SourceVMID },
		"root target":             func(record *VMExecAuditRecord) { record.TargetUser = "root" },
		"invalid target":          func(record *VMExecAuditRecord) { record.TargetUser = "Bad User" },
		"oversized target": func(record *VMExecAuditRecord) {
			record.TargetUser = "abcdefghijklmnopqrstuvwxyzabcdefg"
		},
		"zero start":        func(record *VMExecAuditRecord) { record.StartedAt = time.Time{} },
		"negative duration": func(record *VMExecAuditRecord) { record.Duration = -time.Nanosecond },
		"long duration": func(record *VMExecAuditRecord) {
			record.Duration = vmexecwire.MaxExecutionDuration + time.Nanosecond
		},
		"unknown result":       func(record *VMExecAuditRecord) { record.Result = "other" },
		"success without exit": func(record *VMExecAuditRecord) { record.ExitCode = nil },
		"success nonzero":      func(record *VMExecAuditRecord) { record.ExitCode = intPointer(1) },
		"command failure zero": func(record *VMExecAuditRecord) {
			record.Result, record.ExitCode = VMExecAuditCommandFailed, intPointer(0)
		},
		"command failure too large": func(record *VMExecAuditRecord) {
			record.Result, record.ExitCode = VMExecAuditCommandFailed, intPointer(256)
		},
		"denied with exit": func(record *VMExecAuditRecord) {
			record.Result, record.ExitCode = VMExecAuditDenied, intPointer(1)
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			writer := &fakeVMExecAuditWriter{}
			sink := &VMExecAuditSink{writer: writer}
			record := validVMExecAuditRecord()
			mutate(&record)
			err := sink.Record(t.Context(), record)
			assertVMExecAuditError(t, err, errs.CodeVsockAuditInvalid, errs.ClassValidation)
			assert.Empty(t, writer.records)
		})
	}
}

func TestVMExecAuditRecordRejectsInvalidContextAndUnavailableWriter(t *testing.T) {
	record := validVMExecAuditRecord()
	sink := &VMExecAuditSink{writer: &fakeVMExecAuditWriter{}}
	_, err := NewVMExecAuditSink(nil)
	assertVMExecAuditError(t, err, errs.CodeVsockAuditInvalid, errs.ClassValidation)
	assertVMExecAuditError(t, sink.Record(nil, record), errs.CodeVsockAuditInvalid, errs.ClassValidation)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = NewVMExecAuditSink(ctx)
	assertVMExecAuditError(t, err, errs.CodeVsockAuditInvalid, errs.ClassValidation)
	assertVMExecAuditError(t, sink.Record(ctx, record), errs.CodeVsockAuditInvalid, errs.ClassValidation)

	assertVMExecAuditError(
		t,
		(&VMExecAuditSink{}).Record(t.Context(), record),
		errs.CodeVsockAuditFailed,
		errs.ClassInternal,
	)
}

func TestVMExecAuditRecordSurfacesWriterFailures(t *testing.T) {
	cause := errors.New("write failed")
	for name, writer := range map[string]*fakeVMExecAuditWriter{
		"write error": {writeErr: cause},
		"short write": {writeN: 1},
	} {
		t.Run(name, func(t *testing.T) {
			sink := &VMExecAuditSink{writer: writer}
			err := sink.Record(t.Context(), validVMExecAuditRecord())
			assertVMExecAuditError(t, err, errs.CodeVsockAuditFailed, errs.ClassInternal)
			if name == "write error" {
				assert.ErrorIs(t, err, cause)
			} else {
				assert.ErrorIs(t, err, io.ErrShortWrite)
			}
		})
	}
}

func TestVMExecAuditSinkConstructionAndCloseFailuresAreTyped(t *testing.T) {
	_, err := newVMExecAuditSink(t.Context(), filepath.Join(t.TempDir(), "missing", "audit.log"))
	assertVMExecAuditError(t, err, errs.CodeVsockAuditFailed, errs.ClassInternal)

	cause := errors.New("close failed")
	writer := &fakeVMExecAuditWriter{closeErr: cause}
	sink := &VMExecAuditSink{writer: writer}
	err = sink.Close(t.Context())
	assertVMExecAuditError(t, err, errs.CodeVsockAuditFailed, errs.ClassInternal)
	assert.ErrorIs(t, err, cause)
	assert.Equal(t, 1, writer.closeCalls)

	assertVMExecAuditError(t, sink.Close(nil), errs.CodeVsockAuditInvalid, errs.ClassValidation)
}

// Rationale: cleanup must release the file descriptors even when the operation context has already been cancelled.
func TestVMExecAuditSinkCloseIgnoresCancellationForCleanup(t *testing.T) {
	writer := &fakeVMExecAuditWriter{}
	sink := &VMExecAuditSink{writer: writer}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	err := sink.Close(ctx)
	assertVMExecAuditError(t, err, errs.CodeVsockAuditFailed, errs.ClassInternal)
	assert.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, 1, writer.closeCalls)
}

func TestVMExecAuditSinkConcurrentRecordsRemainWhole(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.log")
	sink, err := newVMExecAuditSink(t.Context(), path)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sink.Close(context.WithoutCancel(t.Context()))) })

	const recordCount = 32
	var wait sync.WaitGroup
	errCh := make(chan error, recordCount)
	for index := range recordCount {
		wait.Add(1)
		go func() {
			defer wait.Done()
			record := validVMExecAuditRecord()
			record.RequestID = requestIDForIndex(index)
			errCh <- sink.Record(t.Context(), record)
		}()
	}
	wait.Wait()
	close(errCh)
	for recordErr := range errCh {
		require.NoError(t, recordErr)
	}

	contents, err := readAuditRecords(path)
	require.NoError(t, err)
	assert.Len(t, contents, recordCount)
}

func intPointer(value int) *int { return &value }

func mapKeys(values map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	return keys
}

func requestIDForIndex(index int) string {
	const hex = "0123456789abcdef"
	value := []byte(testAuditRequestID)
	value[len(value)-1] = hex[index%len(hex)]
	value[len(value)-2] = hex[(index/len(hex))%len(hex)]
	return string(value)
}

func readAuditRecords(path string) ([]string, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	trimmed := strings.TrimSuffix(string(contents), "\n")
	if trimmed == "" {
		return nil, nil
	}
	return strings.Split(trimmed, "\n"), nil
}
