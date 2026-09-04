package logging

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
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
	// mu guards every field below and permits lifecycle assertions after concurrent calls.
	mu           sync.Mutex
	records      [][]byte
	writeN       int
	writeErr     error
	closeErr     error
	closeCalls   int
	writeStarted chan struct{}
	writeRelease chan struct{}
	closeStarted chan struct{}
}

func (writer *fakeVMExecAuditWriter) WriteContext(ctx context.Context, value []byte) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	writer.mu.Lock()
	writeStarted := writer.writeStarted
	writeRelease := writer.writeRelease
	writer.mu.Unlock()
	if writeStarted != nil {
		select {
		case writeStarted <- struct{}{}:
		default:
		}
	}
	if writeRelease != nil {
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-writeRelease:
		}
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
	if writer.closeStarted != nil {
		select {
		case writer.closeStarted <- struct{}{}:
		default:
		}
	}
	return writer.closeErr
}

// validVMExecAuditRecord returns one independently specified valid record that tests may mutate.
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

func (writer *fakeVMExecAuditWriter) snapshot() ([][]byte, int) {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	records := make([][]byte, len(writer.records))
	for index := range writer.records {
		records[index] = append([]byte(nil), writer.records[index]...)
	}
	return records, writer.closeCalls
}

func (writer *fakeVMExecAuditWriter) setWriteResult(written int, err error) {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	writer.writeN = written
	writer.writeErr = err
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
	sink := newVMExecAuditSinkWithWriter(writer)

	require.NoError(t, sink.Record(t.Context(), validVMExecAuditRecord()))
	records, _ := writer.snapshot()
	require.Len(t, records, 1)
	var want map[string]json.RawMessage
	require.NoError(t, json.Unmarshal([]byte(`{
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
	}`), &want))
	assert.Equal(t, byte('\n'), records[0][len(records[0])-1])

	var got map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(records[0], &got))
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("VM-exec audit projection mismatch (-want +got):\n%s", diff)
	}
}

func TestVMExecAuditRecordAcceptsClosedResultVariants(t *testing.T) {
	tests := map[string]struct {
		result   VMExecAuditResult
		exitCode *int
		duration time.Duration
		want     vmExecAuditWireRecord
	}{
		"succeeded": {
			result: VMExecAuditSucceeded, exitCode: intPointer(0),
			want: expectedVMExecAuditWireRecord(VMExecAuditSucceeded, intPointer(0), 1500),
		},
		"command failed min": {
			result: VMExecAuditCommandFailed, exitCode: intPointer(1),
			want: expectedVMExecAuditWireRecord(VMExecAuditCommandFailed, intPointer(1), 1500),
		},
		"command failed max": {
			result: VMExecAuditCommandFailed, exitCode: intPointer(255),
			want: expectedVMExecAuditWireRecord(VMExecAuditCommandFailed, intPointer(255), 1500),
		},
		"denied": {
			result: VMExecAuditDenied,
			want:   expectedVMExecAuditWireRecord(VMExecAuditDenied, nil, 1500),
		},
		"cancelled": {
			result: VMExecAuditCancelled,
			want:   expectedVMExecAuditWireRecord(VMExecAuditCancelled, nil, 1500),
		},
		"timed out": {
			result: VMExecAuditTimedOut, duration: vmexecwire.MaxExecutionDuration,
			want: expectedVMExecAuditWireRecord(
				VMExecAuditTimedOut,
				nil,
				vmexecwire.MaxExecutionDuration.Milliseconds(),
			),
		},
		"relay failed": {
			result: VMExecAuditRelayFailed,
			want:   expectedVMExecAuditWireRecord(VMExecAuditRelayFailed, nil, 1500),
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			writer := &fakeVMExecAuditWriter{}
			sink := newVMExecAuditSinkWithWriter(writer)
			record := validVMExecAuditRecord()
			record.Result = test.result
			record.ExitCode = test.exitCode
			if test.duration != 0 {
				record.Duration = test.duration
			}
			require.NoError(t, sink.Record(t.Context(), record))
			records, _ := writer.snapshot()
			require.Len(t, records, 1)
			var got vmExecAuditWireRecord
			require.NoError(t, json.Unmarshal(records[0], &got))
			if diff := cmp.Diff(test.want, got); diff != "" {
				t.Errorf("VM-exec audit result projection mismatch (-want +got):\n%s", diff)
			}
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
			sink := newVMExecAuditSinkWithWriter(writer)
			record := validVMExecAuditRecord()
			mutate(&record)
			err := sink.Record(t.Context(), record)
			assertVMExecAuditError(t, err, errs.CodeVsockAuditInvalid, errs.ClassValidation)
			records, _ := writer.snapshot()
			assert.Len(t, records, 0)
		})
	}
}

func TestVMExecAuditRecordRejectsInvalidContextAndUnavailableWriter(t *testing.T) {
	record := validVMExecAuditRecord()
	sink := newVMExecAuditSinkWithWriter(&fakeVMExecAuditWriter{})
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

func TestVMExecAuditSinkUsesDedicatedUnprivilegedLog(t *testing.T) {
	// Rationale: sharing the legacy newline-bearing text log would let an attacker-controlled path forge a typed JSON
	// audit record, while privileged pathname construction could redirect root writes through a replaced ancestor.
	cacheDir := t.TempDir()
	require.NoError(t, validateVMExecAuditOwner(1000))
	sink, err := newVMExecAuditSinkInCache(t.Context(), cacheDir)
	require.NoError(t, err)
	require.NoError(t, sink.Record(t.Context(), validVMExecAuditRecord()))
	require.NoError(t, sink.Close(t.Context()))

	dedicatedRecords, err := readAuditRecords(filepath.Join(cacheDir, "vm-exec-audit.jsonl"))
	require.NoError(t, err)
	require.Len(t, dedicatedRecords, 1)
	_, legacyErr := os.Stat(filepath.Join(cacheDir, "audit.log"))
	assert.ErrorIs(t, legacyErr, os.ErrNotExist)

	err = validateVMExecAuditOwner(0)
	assertVMExecAuditError(t, err, errs.CodeVsockAuditFailed, errs.ClassInternal)
	assert.ErrorIs(t, err, errVMExecAuditPrivilegedConstruction)
}

func TestVMExecAuditRecordSurfacesWriterFailures(t *testing.T) {
	// Rationale: after an incomplete or uncertain audit write, accepting a later record could merge it into a partial
	// JSON line and make both events unauthoritative.
	cause := errors.New("write failed")
	for name, test := range map[string]struct {
		writer  *fakeVMExecAuditWriter
		wantErr error
	}{
		"write error": {writer: &fakeVMExecAuditWriter{writeErr: cause}, wantErr: cause},
		"short write": {writer: &fakeVMExecAuditWriter{writeN: 1}, wantErr: io.ErrShortWrite},
	} {
		t.Run(name, func(t *testing.T) {
			sink := newVMExecAuditSinkWithWriter(test.writer)
			firstErr := sink.Record(t.Context(), validVMExecAuditRecord())
			assertVMExecAuditError(t, firstErr, errs.CodeVsockAuditFailed, errs.ClassInternal)
			assert.ErrorIs(t, firstErr, test.wantErr)

			test.writer.setWriteResult(0, nil)
			secondErr := sink.Record(t.Context(), validVMExecAuditRecord())
			assertVMExecAuditError(t, secondErr, errs.CodeVsockAuditFailed, errs.ClassInternal)
			assert.ErrorIs(t, secondErr, test.wantErr)
			assert.Same(t, errs.AsDomainError(firstErr), errs.AsDomainError(secondErr))
			records, _ := test.writer.snapshot()
			assert.Len(t, records, 1)
		})
	}
}

func TestVMExecAuditSinkConstructionAndCloseFailuresAreTyped(t *testing.T) {
	_, err := newVMExecAuditSink(t.Context(), filepath.Join(t.TempDir(), "missing", "audit.log"))
	assertVMExecAuditError(t, err, errs.CodeVsockAuditFailed, errs.ClassInternal)

	cause := errors.New("close failed")
	writer := &fakeVMExecAuditWriter{closeErr: cause}
	sink := newVMExecAuditSinkWithWriter(writer)
	err = sink.Close(t.Context())
	assertVMExecAuditError(t, err, errs.CodeVsockAuditFailed, errs.ClassInternal)
	assert.ErrorIs(t, err, cause)
	secondErr := sink.Close(t.Context())
	assertVMExecAuditError(t, secondErr, errs.CodeVsockAuditFailed, errs.ClassInternal)
	assert.ErrorIs(t, secondErr, cause)
	assert.Same(t, errs.AsDomainError(err), errs.AsDomainError(secondErr))
	_, closeCalls := writer.snapshot()
	assert.Equal(t, 1, closeCalls)

	assertVMExecAuditError(t, sink.Close(nil), errs.CodeVsockAuditInvalid, errs.ClassValidation)
}

// Rationale: cleanup must release the file descriptors even when the operation context has already been cancelled.
func TestVMExecAuditSinkCloseIgnoresCancellationForCleanup(t *testing.T) {
	writer := &fakeVMExecAuditWriter{}
	sink := newVMExecAuditSinkWithWriter(writer)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	require.NoError(t, sink.Close(ctx))
	require.NoError(t, sink.Close(t.Context()))
	_, closeCalls := writer.snapshot()
	assert.Equal(t, 1, closeCalls)
}

func TestVMExecAuditSinkConcurrentCopiedCloseCachesFirstResult(t *testing.T) {
	// Rationale: value copies must share one close authority so concurrent cleanup cannot close a reused descriptor.
	cause := errors.New("close failed")
	writer := &fakeVMExecAuditWriter{closeErr: cause}
	sink := newVMExecAuditSinkWithWriter(writer)
	copiedSink := *sink
	cancelledCtx, cancel := context.WithCancel(t.Context())
	cancel()
	activeCtx := t.Context()

	const callerCount = 16
	results := make(chan error, callerCount)
	var wait sync.WaitGroup
	for index := range callerCount {
		wait.Add(1)
		go func() {
			defer wait.Done()
			if index%2 == 0 {
				results <- sink.Close(cancelledCtx)
				return
			}
			results <- copiedSink.Close(activeCtx)
		}()
	}
	wait.Wait()
	close(results)

	var first *errs.DomainError
	for closeErr := range results {
		assertVMExecAuditError(t, closeErr, errs.CodeVsockAuditFailed, errs.ClassInternal)
		assert.ErrorIs(t, closeErr, cause)
		assert.NotErrorIs(t, closeErr, context.Canceled)
		domainErr := errs.AsDomainError(closeErr)
		if first == nil {
			first = domainErr
			continue
		}
		assert.Same(t, first, domainErr)
	}
	_, closeCalls := writer.snapshot()
	assert.Equal(t, 1, closeCalls)
}

func TestVMExecAuditSinkSerializesRecordAndClose(t *testing.T) {
	// Rationale: close racing an admitted record must wait for that complete write, then permanently reject later
	// records instead of closing the descriptor underneath the write.
	writeStarted := make(chan struct{}, 1)
	writeRelease := make(chan struct{})
	closeStarted := make(chan struct{}, 1)
	writer := &fakeVMExecAuditWriter{
		writeStarted: writeStarted,
		writeRelease: writeRelease,
		closeStarted: closeStarted,
	}
	sink := newVMExecAuditSinkWithWriter(writer)
	recordResult := make(chan error, 1)
	go func() { recordResult <- sink.Record(context.Background(), validVMExecAuditRecord()) }()
	waitForVMExecAuditTestSignal(t, writeStarted, "record write")

	closeAttempted := make(chan struct{})
	closeResult := make(chan error, 1)
	go func() {
		close(closeAttempted)
		closeResult <- sink.Close(context.Background())
	}()
	waitForVMExecAuditTestSignal(t, closeAttempted, "close attempt")
	select {
	case <-closeStarted:
		t.Fatal("underlying close started before the admitted record completed")
	case <-time.After(50 * time.Millisecond):
	}

	close(writeRelease)
	require.NoError(t, waitForVMExecAuditTestResult(t, recordResult, "record result"))
	require.NoError(t, waitForVMExecAuditTestResult(t, closeResult, "close result"))
	waitForVMExecAuditTestSignal(t, closeStarted, "underlying close")

	records, closeCalls := writer.snapshot()
	assert.Len(t, records, 1)
	assert.Equal(t, 1, closeCalls)
	postCloseErr := sink.Record(t.Context(), validVMExecAuditRecord())
	assertVMExecAuditError(t, postCloseErr, errs.CodeVsockAuditFailed, errs.ClassInternal)
	assert.ErrorIs(t, postCloseErr, os.ErrClosed)
}

func TestVMExecAuditSinkConcurrentRecordsRemainWhole(t *testing.T) {
	// Rationale: concurrent callers must persist every exact record once; a line count alone cannot detect duplicate,
	// malformed, or hardcoded records.
	path := filepath.Join(t.TempDir(), "vm-exec-audit.jsonl")
	sink, err := newVMExecAuditSink(t.Context(), path)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sink.Close(context.WithoutCancel(t.Context()))) })

	const recordCount = 32
	ctx := t.Context()
	var wait sync.WaitGroup
	errCh := make(chan error, recordCount)
	want := make([]vmExecAuditWireRecord, 0, recordCount)
	for index := range recordCount {
		record := validVMExecAuditRecord()
		record.RequestID = requestIDForIndex(index)
		want = append(
			want,
			expectedVMExecAuditWireRecord(record.Result, record.ExitCode, record.Duration.Milliseconds()),
		)
		want[len(want)-1].RequestID = record.RequestID
		wait.Add(1)
		go func(record VMExecAuditRecord) {
			defer wait.Done()
			errCh <- sink.Record(ctx, record)
		}(record)
	}
	wait.Wait()
	close(errCh)
	for recordErr := range errCh {
		require.NoError(t, recordErr)
	}

	contents, err := readAuditRecords(path)
	require.NoError(t, err)
	require.Len(t, contents, recordCount)
	got := make([]vmExecAuditWireRecord, 0, recordCount)
	for _, content := range contents {
		var record vmExecAuditWireRecord
		require.NoError(t, json.Unmarshal([]byte(content), &record))
		got = append(got, record)
	}
	sort.Slice(want, func(i, j int) bool { return want[i].RequestID < want[j].RequestID })
	sort.Slice(got, func(i, j int) bool { return got[i].RequestID < got[j].RequestID })
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("concurrent VM-exec audit records mismatch (-want +got):\n%s", diff)
	}
}

func intPointer(value int) *int { return &value }

// expectedVMExecAuditWireRecord derives the fixed wire contract independently from the production encoder.
func expectedVMExecAuditWireRecord(
	result VMExecAuditResult,
	exitCode *int,
	durationMS int64,
) vmExecAuditWireRecord {
	return vmExecAuditWireRecord{
		SchemaVersion:   1,
		Operation:       "vm_exec",
		RequestID:       testAuditRequestID,
		SourceVMID:      testAuditSourceVMID,
		DestinationVMID: testAuditDestinationID,
		TargetUser:      "builder",
		StartedAt:       "2026-09-04T08:11:12Z",
		DurationMS:      durationMS,
		Result:          result,
		ExitCode:        exitCode,
	}
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

// waitForVMExecAuditTestSignal bounds synchronization waits without calling testing.T from worker goroutines.
func waitForVMExecAuditTestSignal(t *testing.T, signal <-chan struct{}, name string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for %s", name)
	}
}

// waitForVMExecAuditTestResult bounds result joins without leaking a failed worker.
func waitForVMExecAuditTestResult(t *testing.T, result <-chan error, name string) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for %s", name)
		return nil
	}
}
