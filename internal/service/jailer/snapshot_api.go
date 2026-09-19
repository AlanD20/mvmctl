package jailer

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"golang.org/x/sys/unix"

	"mvmctl/internal/infra"
)

type snapshotAPISession struct {
	connection  net.Conn
	peerPIDFD   int
	directoryFD int
	record      instanceRecord
	processes   *reopenedRecordedProcesses
}

// CALLER MUST HOLD: the source VM lock and all disk capture locks until publication/resume.
// The session never reconnects or retries a possibly applied mutation.
func openSnapshotAPISession(
	ctx context.Context,
	record instanceRecord,
	processes *reopenedRecordedProcesses,
) (_ *snapshotAPISession, returnErr error) {
	directory, err := pinVMAPIPrivateDirectory(ctx, record)
	if err != nil {
		return nil, err
	}
	session := &snapshotAPISession{directoryFD: directory, peerPIDFD: -1, record: record, processes: processes}
	defer func() {
		if returnErr != nil {
			returnErr = errors.Join(returnErr, session.Release(context.WithoutCancel(ctx)))
		}
	}()
	path := fmt.Sprintf("/proc/self/fd/%d/%s", directory, infra.VMFirecrackerAPISocketFilename)
	session.connection, err = (&net.Dialer{}).DialContext(ctx, "unix", path)
	if err != nil {
		return nil, vmAPIBrokerError("connect snapshot producer API", err)
	}
	session.peerPIDFD, err = admitVMAPIPeer(ctx, session.connection, record, processes)
	if err != nil {
		return nil, err
	}
	return session, nil
}

func (session *snapshotAPISession) Release(ctx context.Context) error {
	var result error
	if session.connection != nil {
		result = errors.Join(result, session.connection.Close())
		session.connection = nil
	}
	for _, fd := range []*int{&session.peerPIDFD, &session.directoryFD} {
		if *fd >= 0 {
			result = errors.Join(result, unix.Close(*fd))
			*fd = -1
		}
	}
	return result
}

type snapshotAPIOperation uint8

const (
	snapshotAPIState snapshotAPIOperation = iota + 1
	snapshotAPIPause
	snapshotAPIResume
	snapshotAPICapture
)

func (session *snapshotAPISession) capture(ctx context.Context, snapshotID string) error {
	if !isCanonicalManagedResourceID(snapshotID, 64) {
		return instanceValidationError("snapshot producer identity is not canonical")
	}
	_, err := session.apply(ctx, snapshotAPICapture, snapshotID)
	return err
}

func (session *snapshotAPISession) state(ctx context.Context) (string, error) {
	data, err := session.apply(ctx, snapshotAPIState, "")
	if err != nil {
		return "", err
	}
	var state struct {
		State string `json:"state"`
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return "", vmAPIBrokerError("decode snapshot producer state", err)
	}
	if state.State != "Running" && state.State != "Paused" {
		return "", instanceStateError(session.record.vmID, "snapshot producer is neither running nor paused")
	}
	return state.State, nil
}

func (session *snapshotAPISession) pause(ctx context.Context) error {
	_, err := session.apply(ctx, snapshotAPIPause, "")
	return err
}

func (session *snapshotAPISession) resume(ctx context.Context) error {
	_, err := session.apply(ctx, snapshotAPIResume, "")
	return err
}

func snapshotProducerPath(snapshotID string) string {
	return managedVMPrivateRuntimeMountPath + "/capture-" + snapshotID
}

func (session *snapshotAPISession) apply(
	ctx context.Context,
	operation snapshotAPIOperation,
	snapshotID string,
) (_ []byte, returnErr error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if session.connection == nil || session.peerPIDFD < 0 {
		return nil, instanceStateError(session.record.vmID, "snapshot API session is closed")
	}
	var method, path string
	var body []byte
	expectedStatus := http.StatusNoContent
	switch operation {
	case snapshotAPIState:
		method, path, expectedStatus = http.MethodGet, "/", http.StatusOK
	case snapshotAPIPause:
		method, path, body = http.MethodPatch, "/vm", []byte(`{"state":"Paused"}`)
	case snapshotAPIResume:
		method, path, body = http.MethodPatch, "/vm", []byte(`{"state":"Resumed"}`)
	case snapshotAPICapture:
		method, path = http.MethodPut, "/snapshot/create"
		var err error
		body, err = json.Marshal(struct {
			Type   string `json:"snapshot_type"`
			Memory string `json:"mem_file_path"`
			State  string `json:"snapshot_path"`
		}{"Full", snapshotProducerPath(snapshotID) + "/memory", snapshotProducerPath(snapshotID) + "/vmstate"})
		if err != nil {
			return nil, err
		}
	default:
		return nil, instanceValidationError("snapshot API operation is unsupported")
	}
	return session.exchange(ctx, method, path, body, expectedStatus)
}

// exchange is private framing shared by the closed snapshot operations, never a wire passthrough.
func (session *snapshotAPISession) exchange(
	ctx context.Context,
	method, path string,
	body []byte,
	expectedStatus int,
) (_ []byte, returnErr error) {
	if err := verifyVMExecFirecrackerPeerIdentity(ctx, realVMExecPeerAuthorityDeps(), realProcessIdentityVerifierDeps(),
		session.record, session.processes, session.peerPIDFD, "snapshot"); err != nil {
		return nil, err
	}
	deadline, _ := ctx.Deadline()
	if err := session.connection.SetDeadline(deadline); err != nil {
		return nil, err
	}
	connection := session.connection
	interrupted := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		defer close(interrupted)
		// Release owns the connection; a deadline interrupts in-flight I/O without reconnecting.
		_ = connection.SetDeadline(time.Now())
	})
	defer func() {
		if !stop() {
			<-interrupted
		}
	}()
	request, err := http.NewRequestWithContext(ctx, method, "http://firecracker"+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	if err := request.Write(session.connection); err != nil {
		return nil, vmAPIBrokerError("snapshot API operation outcome is unknown", errors.Join(err, ctx.Err()))
	}
	response, err := http.ReadResponse(bufio.NewReader(io.LimitReader(session.connection, 64<<10)), request)
	if err != nil {
		return nil, vmAPIBrokerError("snapshot API operation outcome is unknown", errors.Join(err, ctx.Err()))
	}
	defer func() { returnErr = errors.Join(returnErr, response.Body.Close()) }()
	data, err := io.ReadAll(io.LimitReader(response.Body, (64<<10)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > 64<<10 || response.StatusCode != expectedStatus {
		return nil, vmAPIBrokerError("snapshot producer rejected the named operation", nil)
	}
	if err := verifyVMExecFirecrackerPeerIdentity(ctx, realVMExecPeerAuthorityDeps(), realProcessIdentityVerifierDeps(),
		session.record, session.processes, session.peerPIDFD, "snapshot"); err != nil {
		return nil, err
	}
	return data, nil
}
