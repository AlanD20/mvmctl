package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/lib/vmexecwire"
)

type fakePinnedProcess struct {
	mu       sync.Mutex
	aliveErr error
	closed   bool
}

func (pin *fakePinnedProcess) Alive(context.Context) error {
	pin.mu.Lock()
	defer pin.mu.Unlock()
	if pin.closed {
		return errors.New("pin closed")
	}
	return pin.aliveErr
}

func (pin *fakePinnedProcess) Close() error {
	pin.mu.Lock()
	pin.closed = true
	pin.mu.Unlock()
	return nil
}

type fakeSourceProcessEffects struct {
	mu sync.Mutex

	account       sourceAccount
	accountErr    error
	accountCalls  int
	credentials   peerCredentials
	credentialErr error
	pins          map[int]*fakePinnedProcess
	identities    map[int][]processIdentity
	identityFunc  func(int) processIdentity
	readCounts    map[int]int
}

func (effects *fakeSourceProcessEffects) ResolveAccount(
	context.Context,
	string,
) (sourceAccount, error) {
	effects.mu.Lock()
	effects.accountCalls++
	effects.mu.Unlock()
	return effects.account, effects.accountErr
}

func (effects *fakeSourceProcessEffects) PeerCredentials(
	context.Context,
	net.Conn,
) (peerCredentials, error) {
	return effects.credentials, effects.credentialErr
}

func (effects *fakeSourceProcessEffects) Pin(_ context.Context, intPID int) (pinnedProcess, error) {
	effects.mu.Lock()
	defer effects.mu.Unlock()
	if effects.pins == nil {
		effects.pins = make(map[int]*fakePinnedProcess)
	}
	pin, exists := effects.pins[intPID]
	if !exists {
		pin = &fakePinnedProcess{}
		effects.pins[intPID] = pin
	}
	return pin, nil
}

func (effects *fakeSourceProcessEffects) ReadIdentity(
	_ context.Context,
	pid int,
) (processIdentity, error) {
	effects.mu.Lock()
	defer effects.mu.Unlock()
	values := effects.identities[pid]
	if len(values) == 0 {
		if effects.identityFunc != nil {
			return effects.identityFunc(pid), nil
		}
		return processIdentity{}, errors.New("missing fake identity")
	}
	if effects.readCounts == nil {
		effects.readCounts = make(map[int]int)
	}
	index := effects.readCounts[pid]
	effects.readCounts[pid]++
	if index >= len(values) {
		index = len(values) - 1
	}
	return values[index], nil
}

func TestProcessTreeAdmission(t *testing.T) {
	root := processIdentity{PID: 100, ParentPID: 1, StartTime: 10}
	tests := map[string]struct {
		identities map[int][]processIdentity
		peerPID    int
		peerErr    error
		rootErr    error
		want       bool
		wantErr    string
	}{
		"direct descendant": {
			identities: map[int][]processIdentity{100: {{PID: 100, ParentPID: 1, StartTime: 10}}},
			peerPID:    100,
			want:       true,
		},
		"shell descendant": {
			identities: map[int][]processIdentity{
				300: {{PID: 300, ParentPID: 200, StartTime: 30}},
				200: {{PID: 200, ParentPID: 100, StartTime: 20}},
				100: {root},
			},
			peerPID: 300,
			want:    true,
		},
		"su descendant": {
			identities: map[int][]processIdentity{
				400: {{PID: 400, ParentPID: 300, StartTime: 40}},
				300: {{PID: 300, ParentPID: 200, StartTime: 30}},
				200: {{PID: 200, ParentPID: 100, StartTime: 20}},
				100: {root},
			},
			peerPID: 400,
			want:    true,
		},
		"sibling same UID": {
			identities: map[int][]processIdentity{
				300: {{PID: 300, ParentPID: 50, StartTime: 30}},
				50:  {{PID: 50, ParentPID: 1, StartTime: 5}},
			},
			peerPID: 300,
		},
		"reparented during admission": {
			identities: map[int][]processIdentity{
				300: {
					{PID: 300, ParentPID: 100, StartTime: 30},
					{PID: 300, ParentPID: 50, StartTime: 30},
				},
				100: {root},
				50:  {{PID: 50, ParentPID: 1, StartTime: 5}},
			},
			peerPID: 300,
			wantErr: "changed during admission",
		},
		"reused root PID": {
			identities: map[int][]processIdentity{
				300: {{PID: 300, ParentPID: 100, StartTime: 30}},
				100: {
					root,
					{PID: 100, ParentPID: 1, StartTime: 11},
				},
			},
			peerPID: 300,
			wantErr: "changed during admission",
		},
		"malformed identity": {
			identities: map[int][]processIdentity{300: {{PID: 300, ParentPID: 100, StartTime: 0}}},
			peerPID:    300,
			wantErr:    "malformed",
		},
		"cycle": {
			identities: map[int][]processIdentity{
				300: {{PID: 300, ParentPID: 200, StartTime: 30}},
				200: {{PID: 200, ParentPID: 300, StartTime: 20}},
			},
			peerPID: 300,
			wantErr: "cycle",
		},
		"dead peer": {
			identities: map[int][]processIdentity{300: {{PID: 300, ParentPID: 100, StartTime: 30}}},
			peerPID:    300,
			peerErr:    errors.New("dead"),
			wantErr:    "not live",
		},
		"dead root": {
			identities: map[int][]processIdentity{300: {{PID: 300, ParentPID: 100, StartTime: 30}}},
			peerPID:    300,
			rootErr:    errors.New("dead"),
			wantErr:    "not live",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			effects := &fakeSourceProcessEffects{identities: tc.identities}
			peerPin := &fakePinnedProcess{aliveErr: tc.peerErr}
			rootPin := &fakePinnedProcess{aliveErr: tc.rootErr}
			got, err := processDescendsFrom(
				t.Context(), effects, peerPin, tc.peerPID, rootPin, root,
			)
			if tc.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestProcessTreeAdmissionRejectsDepthLimit(t *testing.T) {
	identities := make(map[int][]processIdentity)
	for pid := 1000; pid > 900; pid-- {
		identities[pid] = []processIdentity{{PID: pid, ParentPID: pid - 1, StartTime: uint64(pid)}}
	}
	effects := &fakeSourceProcessEffects{identities: identities}

	_, err := processDescendsFrom(
		t.Context(),
		effects,
		&fakePinnedProcess{},
		1000,
		&fakePinnedProcess{},
		processIdentity{PID: 10, ParentPID: 1, StartTime: 10},
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exceeds 64")
}

func TestParseProcessStatHandlesNamesAndRejectsMalformedValues(t *testing.T) {
	fields := []string{"S", "1"}
	fields = append(fields, make([]string, 17)...)
	for index := 2; index < len(fields); index++ {
		fields[index] = "0"
	}
	fields = append(fields, "99")
	identity, err := parseProcessStat([]byte(fmt.Sprintf("123 (shell ) worker) %s\n", strings.Join(fields, " "))))
	require.NoError(t, err)
	assert.Equal(t, processIdentity{PID: 123, ParentPID: 1, StartTime: 99}, identity)

	for name, value := range map[string]string{
		"missing close":   "123 (shell S 1",
		"bad PID":         "bad (shell) " + strings.Join(fields, " "),
		"missing fields":  "123 (shell) S 1",
		"zero start time": "123 (shell) " + strings.Join(append(fields[:19], "0"), " "),
		"negative parent": "123 (shell) S -1 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 99",
	} {
		t.Run(name, func(t *testing.T) {
			_, parseErr := parseProcessStat([]byte(value))
			require.Error(t, parseErr)
		})
	}
}

func TestSourceClaimRequiresExactCredentialsOneMatchAndOneShot(t *testing.T) {
	effects := &fakeSourceProcessEffects{
		credentials: peerCredentials{PID: 300, UID: 1001, GID: 1002},
		identities: map[int][]processIdentity{
			300: {{PID: 300, ParentPID: 100, StartTime: 30}},
			100: {{PID: 100, ParentPID: 1, StartTime: 10}},
		},
	}
	rootPin := &fakePinnedProcess{}
	binding := testPublishedBinding(
		"00112233445566778899aabbccddeeff",
		sourceAccount{UID: 1001, GID: 1002},
		processIdentity{PID: 100, ParentPID: 1, StartTime: 10},
		rootPin,
	)
	agent := testSourceAgent(effects, binding)
	local, peer := net.Pipe()
	t.Cleanup(func() { _ = local.Close() })
	t.Cleanup(func() { _ = peer.Close() })

	got, err := agent.claimSourceForPeer(t.Context(), local)
	require.NoError(t, err)
	assert.Same(t, binding, got)
	_, err = agent.claimSourceForPeer(t.Context(), local)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a descendant")

	agent.cleanupSourceBinding(binding)
	assert.True(t, rootPin.closed)
}

func TestSourceClaimRejectsUIDGIDAndMultipleMatches(t *testing.T) {
	tests := map[string]struct {
		credentials peerCredentials
		bindings    []*sourceBinding
		wantErr     string
	}{
		"UID mismatch": {
			credentials: peerCredentials{PID: 300, UID: 2001, GID: 1002},
			bindings: []*sourceBinding{testPublishedBinding(
				"00112233445566778899aabbccddeeff",
				sourceAccount{UID: 1001, GID: 1002},
				processIdentity{PID: 100, ParentPID: 1, StartTime: 10},
				&fakePinnedProcess{},
			)},
			wantErr: "not a descendant",
		},
		"GID mismatch": {
			credentials: peerCredentials{PID: 300, UID: 1001, GID: 2002},
			bindings: []*sourceBinding{testPublishedBinding(
				"00112233445566778899aabbccddeeff",
				sourceAccount{UID: 1001, GID: 1002},
				processIdentity{PID: 100, ParentPID: 1, StartTime: 10},
				&fakePinnedProcess{},
			)},
			wantErr: "not a descendant",
		},
		"multiple matches": {
			credentials: peerCredentials{PID: 300, UID: 1001, GID: 1002},
			bindings: []*sourceBinding{
				testPublishedBinding(
					"00112233445566778899aabbccddeeff",
					sourceAccount{UID: 1001, GID: 1002},
					processIdentity{PID: 100, ParentPID: 1, StartTime: 10},
					&fakePinnedProcess{},
				),
				testPublishedBinding(
					"ffeeddccbbaa99887766554433221100",
					sourceAccount{UID: 1001, GID: 1002},
					processIdentity{PID: 100, ParentPID: 1, StartTime: 10},
					&fakePinnedProcess{},
				),
			},
			wantErr: "multiple",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			effects := &fakeSourceProcessEffects{
				credentials: tc.credentials,
				identities: map[int][]processIdentity{
					300: {{PID: 300, ParentPID: 100, StartTime: 30}},
					100: {{PID: 100, ParentPID: 1, StartTime: 10}},
				},
			}
			agent := testSourceAgent(effects, tc.bindings...)
			local, peer := net.Pipe()
			defer local.Close()
			defer peer.Close()
			_, err := agent.claimSourceForPeer(t.Context(), local)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

func TestLocalPeerMayArriveBeforeSourceCommandPublication(t *testing.T) {
	effects := &fakeSourceProcessEffects{
		credentials: peerCredentials{PID: 300, UID: 1001, GID: 1002},
		identities: map[int][]processIdentity{
			300: {{PID: 300, ParentPID: 100, StartTime: 30}},
			100: {{PID: 100, ParentPID: 1, StartTime: 10}},
		},
	}
	binding := &sourceBinding{
		sessionID: "00112233445566778899aabbccddeeff",
		transport: newSourceTransport(&discardConn{}),
		account:   sourceAccount{UID: 1001, GID: 1002},
		published: make(chan struct{}),
	}
	agent := testSourceAgent(effects, binding)
	local, peer := net.Pipe()
	defer local.Close()
	defer peer.Close()

	claimed := make(chan error, 1)
	go func() {
		_, err := agent.claimSourceForPeer(context.Background(), local)
		claimed <- err
	}()
	time.Sleep(10 * time.Millisecond)
	agent.sourceMu.Lock()
	binding.root = processIdentity{PID: 100, ParentPID: 1, StartTime: 10}
	binding.rootPin = &fakePinnedProcess{}
	close(binding.published)
	agent.notifySourceChangeLocked()
	agent.sourceMu.Unlock()
	assert.NoError(t, <-claimed)
	agent.cleanupSourceBinding(binding)
}

func TestLocalSourceRelayUsesVMExecWireAndIsBinarySafe(t *testing.T) {
	effects := &fakeSourceProcessEffects{
		credentials: peerCredentials{PID: 300, UID: 1001, GID: 1002},
		identities: map[int][]processIdentity{
			300: {{PID: 300, ParentPID: 100, StartTime: 30}},
			100: {{PID: 100, ParentPID: 1, StartTime: 10}},
		},
	}
	sourceAgent, sourceHost := net.Pipe()
	binding := testPublishedBinding(
		"00112233445566778899aabbccddeeff",
		sourceAccount{UID: 1001, GID: 1002},
		processIdentity{PID: 100, ParentPID: 1, StartTime: 10},
		&fakePinnedProcess{},
	)
	binding.transport = newSourceTransport(sourceAgent)
	agent := testSourceAgent(effects, binding)
	localAgent, localCLI := net.Pipe()
	handlerDone := make(chan struct{})
	go func() {
		agent.handleLocalConn(context.Background(), localAgent)
		close(handlerDone)
	}()

	request := vmexecwire.SourceRequest{
		RequestID:   "aabbccddeeff00112233445566778899",
		Destination: "target",
		TargetUser:  "alice",
		Command:     "printf binary",
	}
	require.NoError(t, vmexecwire.EncodeSourceRequest(t.Context(), localCLI, request))
	forwarded, err := vmexecwire.DecodeSourceRequest(t.Context(), sourceHost)
	require.NoError(t, err)
	assert.Equal(t, request, forwarded)

	wantBinary := []byte{0x00, 0xff, '\n', 0x7f}
	responseDone := make(chan error, 1)
	go func() {
		writer, writerErr := vmexecwire.NewResponseWriter(sourceHost, request.RequestID)
		if writerErr == nil {
			writerErr = writer.WriteStdout(context.Background(), wantBinary)
		}
		if writerErr == nil {
			writerErr = writer.WriteResult(context.Background(), 0, time.Millisecond)
		}
		if closeErr := sourceHost.Close(); writerErr == nil {
			writerErr = closeErr
		}
		responseDone <- writerErr
	}()

	reader, err := vmexecwire.NewResponseReader(localCLI, request.RequestID)
	require.NoError(t, err)
	frame, err := reader.Read(t.Context())
	require.NoError(t, err)
	stdout, ok := frame.(vmexecwire.StdoutFrame)
	require.True(t, ok)
	assert.Equal(t, wantBinary, stdout.Data)
	_, err = reader.Read(t.Context())
	require.NoError(t, err)
	_, err = reader.Read(t.Context())
	assert.ErrorIs(t, err, io.EOF)
	assert.NoError(t, <-responseDone)
	_ = localCLI.Close()
	<-handlerDone
}

func TestLocalDisconnectCleansClaimedSourceSession(t *testing.T) {
	effects := &fakeSourceProcessEffects{
		credentials: peerCredentials{PID: 300, UID: 1001, GID: 1002},
		identities: map[int][]processIdentity{
			300: {{PID: 300, ParentPID: 100, StartTime: 30}},
			100: {{PID: 100, ParentPID: 1, StartTime: 10}},
		},
	}
	rootPin := &fakePinnedProcess{}
	sourceAgent, sourceHost := net.Pipe()
	binding := testPublishedBinding(
		"00112233445566778899aabbccddeeff",
		sourceAccount{UID: 1001, GID: 1002},
		processIdentity{PID: 100, ParentPID: 1, StartTime: 10},
		rootPin,
	)
	binding.transport = newSourceTransport(sourceAgent)
	agent := testSourceAgent(effects, binding)
	localAgent, localCLI := net.Pipe()
	handlerDone := make(chan struct{})
	go func() {
		agent.handleLocalConn(context.Background(), localAgent)
		close(handlerDone)
	}()

	request := vmexecwire.SourceRequest{
		RequestID:   "aabbccddeeff00112233445566778899",
		Destination: "target",
		TargetUser:  "alice",
		Command:     "true",
	}
	require.NoError(t, vmexecwire.EncodeSourceRequest(t.Context(), localCLI, request))
	_, err := vmexecwire.DecodeSourceRequest(t.Context(), sourceHost)
	require.NoError(t, err)
	require.NoError(t, localCLI.Close())
	<-handlerDone

	agent.sourceMu.Lock()
	assert.Empty(t, agent.sourceBindings)
	agent.sourceMu.Unlock()
	assert.True(t, rootPin.closed)
	select {
	case <-binding.transport.done:
	default:
		t.Fatal("source transport remained open after local disconnect")
	}
	_ = sourceHost.Close()
}

func TestSourceCommandUsesFreshLinuxSession(t *testing.T) {
	command := exec.Command("sh", "-c", "true")
	configureSourceCommand(command)
	require.NotNil(t, command.SysProcAttr)
	assert.True(t, command.SysProcAttr.Setsid)
}

func TestNormalExecDoesNotTouchSourceSessionEffects(t *testing.T) {
	effects := &fakeSourceProcessEffects{accountErr: errors.New("must not be called")}
	agent := testSourceAgent(effects)
	agent.handleExec(t.Context(), &execRequest{
		ID: "ordinary", Type: requestTypeExec, Command: "true", User: "root", NoSync: true,
	}, &discardConn{}, &sync.Mutex{})

	effects.mu.Lock()
	defer effects.mu.Unlock()
	assert.Zero(t, effects.accountCalls)
	assert.Empty(t, effects.pins)
	assert.Empty(t, agent.pendingSources)
	assert.Empty(t, agent.sourceBindings)
}

func TestSourceExecRejectsMissingSessionBeforeStartingCommand(t *testing.T) {
	effects := &fakeSourceProcessEffects{account: sourceAccount{UID: 1001, GID: 1002}}
	agent := testSourceAgent(effects)
	marker := filepath.Join(t.TempDir(), "started")
	agent.handleExec(t.Context(), &execRequest{
		ID:              "source",
		Type:            requestTypeExec,
		Command:         "touch " + marker,
		User:            "alice",
		NoSync:          true,
		SourceSessionID: "00112233445566778899aabbccddeeff",
	}, &discardConn{}, &sync.Mutex{})

	_, err := os.Stat(marker)
	assert.ErrorIs(t, err, os.ErrNotExist)
}

func testSourceAgent(effects sourceProcessEffects, bindings ...*sourceBinding) *Agent {
	agent := &Agent{
		pendingSources:  make(map[string]*sourceTransport),
		sourceBindings:  make(map[string]*sourceBinding),
		sourceChanged:   make(chan struct{}),
		sourceProcesses: effects,
	}
	for _, binding := range bindings {
		agent.sourceBindings[binding.sessionID] = binding
	}
	return agent
}

func testPublishedBinding(
	sessionID string,
	account sourceAccount,
	root processIdentity,
	rootPin pinnedProcess,
) *sourceBinding {
	published := make(chan struct{})
	close(published)
	return &sourceBinding{
		sessionID: sessionID,
		transport: newSourceTransport(&discardConn{}),
		account:   account,
		published: published,
		root:      root,
		rootPin:   rootPin,
	}
}

type discardConn struct{}

func (*discardConn) Read([]byte) (int, error)         { return 0, errors.New("closed") }
func (*discardConn) Write(value []byte) (int, error)  { return len(value), nil }
func (*discardConn) Close() error                     { return nil }
func (*discardConn) LocalAddr() net.Addr              { return &net.UnixAddr{} }
func (*discardConn) RemoteAddr() net.Addr             { return &net.UnixAddr{} }
func (*discardConn) SetDeadline(time.Time) error      { return nil }
func (*discardConn) SetReadDeadline(time.Time) error  { return nil }
func (*discardConn) SetWriteDeadline(time.Time) error { return nil }
