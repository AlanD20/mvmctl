package jailer

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/pkg/errs"
)

type resumeHTTPObservation struct {
	state     string
	mutations int
	err       error
}

// serveResumeHTTP models Firecracker's fixed GET/PATCH protocol and independently records mutation effects.
func serveResumeHTTP(
	connection net.Conn,
	payload, originalState string,
	patchStatus int,
	loseResponse bool,
) resumeHTTPObservation {
	defer connection.Close()
	observation := resumeHTTPObservation{state: originalState}
	reader := bufio.NewReader(connection)
	for {
		request, err := http.ReadRequest(reader)
		if errors.Is(err, io.EOF) {
			return observation
		}
		if err != nil {
			observation.err = err
			return observation
		}
		body, err := io.ReadAll(request.Body)
		if err != nil {
			observation.err = err
			return observation
		}
		if err := request.Body.Close(); err != nil {
			observation.err = err
			return observation
		}
		status, responseBody := http.StatusOK, payload
		if request.Method == http.MethodPatch && request.URL.Path == "/vm" && string(body) == `{"state":"Resumed"}` {
			observation.mutations++
			status, responseBody = patchStatus, ""
			if patchStatus == http.StatusNoContent {
				observation.state = "Running"
			}
			if loseResponse {
				return observation
			}
		} else if request.Method != http.MethodGet || request.URL.Path != "/" || len(body) != 0 {
			observation.err = fmt.Errorf("unexpected resume request: %s %s %q", request.Method, request.URL.Path, body)
			return observation
		}
		response := &http.Response{StatusCode: status, ProtoMajor: 1, ProtoMinor: 1,
			Header: http.Header{}, Body: io.NopCloser(strings.NewReader(responseBody)),
			ContentLength: int64(len(responseBody))}
		if err := response.Write(connection); err != nil {
			observation.err = err
			return observation
		}
	}
}

func TestVMAPIResumeUsesAuthoritativeState(t *testing.T) {
	// Rationale: Running is idempotent only after the root state observation; cached starting is not authority.
	for name, test := range map[string]struct {
		payload, original, wantState string
		patchStatus                  int
		loseResponse                 bool
		wantCode                     errs.Code
		wantMutations                int
	}{
		"not_started_refused": {payload: `{"state":"Not started"}`, original: "Not started",
			wantState: "Not started", wantCode: errs.CodeVMStateInvalid},
		"missing_state_refused":   {payload: `{}`, original: "Paused", wantState: "Paused", wantCode: errs.CodeVMStateInvalid},
		"malformed_state_refused": {payload: `{"state":`, original: "Paused", wantState: "Paused", wantCode: errs.CodeVMStateInvalid},
		"paused_patch_refused": {payload: `{"state":"Paused"}`, original: "Paused", patchStatus: http.StatusBadRequest,
			wantState: "Paused", wantMutations: 1, wantCode: errs.CodeFirecrackerClientError},
		"lost_patch_response_is_unknown": {payload: `{"state":"Paused"}`, original: "Paused", patchStatus: http.StatusNoContent,
			loseResponse: true, wantState: "Running", wantMutations: 1, wantCode: errs.CodeFirecrackerClientError},
		"running_no_patch": {payload: `{"state":"Running"}`, original: "Running", wantState: "Running"},
		"paused_resumes_once": {payload: `{"state":"Paused"}`, original: "Paused", patchStatus: http.StatusNoContent,
			wantState: "Running", wantMutations: 1},
	} {
		t.Run(name, func(t *testing.T) {
			client, server := net.Pipe()
			t.Cleanup(func() { require.NoError(t, client.Close()) })
			observed := make(chan resumeHTTPObservation, 1)
			go func() {
				observed <- serveResumeHTTP(server, test.payload, test.original, test.patchStatus, test.loseResponse)
			}()
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()
			err := resumeVMAPI(ctx, client, testVMID)
			require.NoError(t, client.Close())
			observation := <-observed
			require.NoError(t, observation.err)
			if test.wantCode != "" {
				require.Error(t, err)
				assert.Equal(t, test.wantCode, errs.AsDomainError(err).Code)
			} else {
				assert.NoError(t, err)
			}
			want := resumeHTTPObservation{state: test.wantState, mutations: test.wantMutations}
			if diff := cmp.Diff(want, observation, cmp.AllowUnexported(resumeHTTPObservation{})); diff != "" {
				t.Errorf("authoritative resume effects (-want +got):\n%s", diff)
			}
		})
	}
}

func TestVMAPIResumeCancellationDoesNotMutate(t *testing.T) {
	// Rationale: Cancellation during the authoritative GET must interrupt I/O without issuing PATCH.
	client, server := net.Pipe()
	t.Cleanup(func() { require.NoError(t, client.Close()); require.NoError(t, server.Close()) })
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	observed := make(chan error, 1)
	go func() {
		request, err := http.ReadRequest(bufio.NewReader(server))
		if err != nil {
			observed <- err
			return
		}
		if request.Method != http.MethodGet || request.URL.Path != "/" {
			observed <- errors.New("resume mutated before observing state")
			return
		}
		observed <- request.Body.Close()
		cancel()
	}()
	err := resumeVMAPI(ctx, client, testVMID)
	assert.ErrorIs(t, err, context.Canceled)
	require.NoError(t, <-observed)
}

func TestVMAPIResumeRootPendingExportRefusesBeforeControl(t *testing.T) {
	// Rationale: Even an active record must refuse Resume before opening its API when export recovery is pending.
	if os.Geteuid() != 0 {
		t.Skip("root export authority runs only in the approved disposable runner")
	}
	authority, root := newTestInstanceAuthority(t)
	record := testActiveInstanceRecord(false)
	writeTestAuthorityRecord(t, root, record)
	before, err := os.ReadFile(filepath.Join(root, "var/lib/mvmctl/instances/1000", testVMID+".json"))
	require.NoError(t, err)
	dirs := &instanceUIDDirectories{
		deps:   realInstanceAuthorityDeps(),
		policy: testInstanceAuthorityPolicy(root),
		uid:    record.ownerUID,
	}
	journal, err := openVMRootfsExportJournal(t.Context(), dirs, testVMID, true)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, journal.close(context.Background())) })
	progress := rootfsExportTestProgress("Running", "pause_inflight")
	progress.OwnerGID = record.launch.ownerGID
	require.NoError(t, journal.write(t.Context(), progress))
	server := &VMAPIBrokerServer{instances: authority}
	_, err = server.Resume(t.Context(), record.ownerUID, record.launch.ownerGID, VMAPIRequest{VMID: testVMID})
	require.Error(t, err)
	domain := errs.AsDomainError(err)
	require.NotNil(t, domain)
	assert.Equal(t, errs.CodeImageImportFailed, domain.Code)
	assert.Equal(t, "unknown", domain.Details["vm_state"])
	after, err := os.ReadFile(filepath.Join(root, "var/lib/mvmctl/instances/1000", testVMID+".json"))
	require.NoError(t, err)
	if diff := cmp.Diff(before, after); diff != "" {
		t.Errorf("resume refusal changed source authority (-want +got):\n%s", diff)
	}
	persisted, err := journal.read(t.Context())
	require.NoError(t, err)
	require.NotNil(t, persisted)
	if diff := cmp.Diff(progress, *persisted, cmpopts.EquateComparable(protectedBackingBinding{})); diff != "" {
		t.Errorf("resume refusal changed pending intent (-want +got):\n%s", diff)
	}
}
