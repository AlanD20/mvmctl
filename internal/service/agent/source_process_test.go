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

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"mvmctl/internal/lib/vmexecwire"
)

type fakePinnedProcess struct {
	mu             sync.Mutex
	aliveErr       error
	aliveSeq       []error
	exited         bool
	exitedSeq      []bool
	aliveCall      int
	aliveBarrierAt int
	aliveReached   chan struct{}
	aliveRelease   <-chan struct{}
	aliveOnce      sync.Once
	closed         bool
	closes         int
	closeErr       error
}

func (pin *fakePinnedProcess) Alive(ctx context.Context) error {
	exited, err := pin.Exited(ctx)
	if err != nil {
		return err
	}
	if exited {
		return errors.New("process has exited")
	}
	return nil
}

func (pin *fakePinnedProcess) Exited(context.Context) (bool, error) {
	pin.mu.Lock()
	if pin.closed {
		pin.mu.Unlock()
		return false, errors.New("pin closed")
	}
	call := pin.aliveCall + 1
	pin.aliveCall++
	aliveErr := pin.aliveErr
	exited := pin.exited
	if len(pin.aliveSeq) > 0 {
		index := call - 1
		if index >= len(pin.aliveSeq) {
			index = len(pin.aliveSeq) - 1
		}
		aliveErr = pin.aliveSeq[index]
	}
	if len(pin.exitedSeq) > 0 {
		index := min(call-1, len(pin.exitedSeq)-1)
		exited = pin.exitedSeq[index]
	}
	barrier := call == pin.aliveBarrierAt && pin.aliveReached != nil
	pin.mu.Unlock()
	if barrier {
		pin.aliveOnce.Do(func() { close(pin.aliveReached) })
		<-pin.aliveRelease
	}
	return exited, aliveErr
}

func (pin *fakePinnedProcess) Close() error {
	pin.mu.Lock()
	pin.closed = true
	pin.closes++
	pin.mu.Unlock()
	return pin.closeErr
}

type fakeSourceProcessEffects struct {
	mu sync.Mutex

	account         sourceAccount
	accountErr      error
	accountCalls    int
	accountNames    []string
	credentials     peerCredentials
	credentialFunc  func(net.Conn) peerCredentials
	credentialErr   error
	credentialsRead chan struct{}
	credentialOnce  sync.Once
	claimArrived    chan struct{}
	claimRelease    <-chan struct{}
	pins            map[int]*fakePinnedProcess
	peerPins        []*fakePinnedProcess
	identities      map[int][]processIdentity
	identityFunc    func(int) processIdentity
	readCounts      map[int]int
}

func (effects *fakeSourceProcessEffects) ResolveAccount(
	_ context.Context,
	username string,
) (sourceAccount, error) {
	effects.mu.Lock()
	effects.accountCalls++
	effects.accountNames = append(effects.accountNames, username)
	effects.mu.Unlock()
	return effects.account, effects.accountErr
}

func (effects *fakeSourceProcessEffects) PeerCredentials(
	_ context.Context,
	conn net.Conn,
) (peerCredentials, pinnedProcess, error) {
	if effects.credentialErr != nil {
		return peerCredentials{}, nil, effects.credentialErr
	}
	effects.mu.Lock()
	pin := &fakePinnedProcess{}
	effects.peerPins = append(effects.peerPins, pin)
	effects.mu.Unlock()
	effects.credentialOnce.Do(func() {
		if effects.credentialsRead != nil {
			close(effects.credentialsRead)
		}
	})
	credentials := effects.credentials
	if effects.credentialFunc != nil {
		credentials = effects.credentialFunc(conn)
	}
	if effects.claimArrived != nil {
		effects.claimArrived <- struct{}{}
		<-effects.claimRelease
	}
	return credentials, pin, nil
}

func (effects *fakeSourceProcessEffects) AdoptProcess(
	_ context.Context,
	intPID int,
	_ int,
) (pinnedProcess, error) {
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

func (effects *fakeSourceProcessEffects) AdoptSourceCommand(
	ctx context.Context,
	pid int,
	pidFD int,
) (pinnedProcess, error) {
	return effects.AdoptProcess(ctx, pid, pidFD)
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

func TestProcessTreeAdmissionRechecksFinalLiveness(t *testing.T) {
	effects := &fakeSourceProcessEffects{identities: map[int][]processIdentity{
		300: {{PID: 300, ParentPID: 100, StartTime: 30}},
		100: {{PID: 100, ParentPID: 1, StartTime: 10}},
	}}
	_, err := processDescendsFrom(
		t.Context(),
		effects,
		&fakePinnedProcess{aliveSeq: []error{nil, nil, errors.New("exited")}},
		300,
		&fakePinnedProcess{},
		processIdentity{PID: 100, ParentPID: 1, StartTime: 10},
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "died before claim")
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
	assert.Empty(t, cmp.Diff(processIdentity{PID: 123, ParentPID: 1, StartTime: 99}, identity))

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
			50:  {{PID: 50, ParentPID: 1, StartTime: 5}},
		},
	}
	unpublishedSnapshotReached := make(chan struct{})
	releaseSnapshot := make(chan struct{})
	var releaseSnapshotOnce sync.Once
	releasePublicationBarrier := func() { releaseSnapshotOnce.Do(func() { close(releaseSnapshot) }) }
	t.Cleanup(releasePublicationBarrier)
	decoy := testPublishedBinding(
		"ffeeddccbbaa99887766554433221100",
		sourceAccount{UID: 1001, GID: 1002},
		processIdentity{PID: 50, ParentPID: 1, StartTime: 5},
		&fakePinnedProcess{
			aliveBarrierAt: 3,
			aliveReached:   unpublishedSnapshotReached,
			aliveRelease:   releaseSnapshot,
		},
	)
	binding := &sourceBinding{
		sessionID: "00112233445566778899aabbccddeeff",
		transport: newSourceTransport(&discardConn{}),
		account:   sourceAccount{UID: 1001, GID: 1002},
		published: make(chan struct{}),
	}
	agent := testSourceAgent(effects, decoy, binding)
	local, peer := net.Pipe()
	defer local.Close()
	defer peer.Close()

	claimed := make(chan error, 1)
	go func() {
		_, err := agent.claimSourceForPeer(context.Background(), local)
		claimed <- err
	}()
	select {
	case <-unpublishedSnapshotReached:
	case <-time.After(time.Second):
		t.Fatal("claimant did not inspect an unpublished source snapshot")
	}
	select {
	case err := <-claimed:
		t.Fatalf("claim completed before source publication: %v", err)
	default:
	}
	agent.sourceMu.Lock()
	binding.root = processIdentity{PID: 100, ParentPID: 1, StartTime: 10}
	binding.rootPin = &fakePinnedProcess{}
	close(binding.published)
	agent.notifySourceChangeLocked()
	agent.sourceMu.Unlock()
	releasePublicationBarrier()
	select {
	case claimErr := <-claimed:
		assert.NoError(t, claimErr)
	case <-time.After(time.Second):
		t.Fatal("claimant did not resume after source publication")
	}
	agent.cleanupSourceBinding(binding)
	agent.cleanupSourceBinding(decoy)
}

func TestTransportCleanupInterruptsClaimedPartialRequest(t *testing.T) {
	effects := &fakeSourceProcessEffects{
		credentials: peerCredentials{PID: 300, UID: 1001, GID: 1002},
		identities: map[int][]processIdentity{
			300: {{PID: 300, ParentPID: 100, StartTime: 30}},
			100: {{PID: 100, ParentPID: 1, StartTime: 10}},
		},
	}
	sourceAgent, sourceHost := net.Pipe()
	t.Cleanup(func() { _ = sourceHost.Close() })
	binding := testPublishedBinding(
		"00112233445566778899aabbccddeeff",
		sourceAccount{UID: 1001, GID: 1002},
		processIdentity{PID: 100, ParentPID: 1, StartTime: 10},
		&fakePinnedProcess{},
	)
	binding.transport = newSourceTransport(sourceAgent)
	agent := testSourceAgent(effects, binding)
	localAgent, localCLI := net.Pipe()
	t.Cleanup(func() { _ = localCLI.Close() })
	done := make(chan struct{})
	go func() {
		agent.handleLocalConn(context.Background(), localAgent)
		close(done)
	}()

	waitForSourceClaim(t, agent, binding)
	agent.cleanupSourceBinding(binding)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("transport cleanup did not interrupt a claimed partial request")
	}
}

func TestConcurrentSourceClaimsHaveExactlyOneWinnerAndClosePeerPinsOnce(t *testing.T) {
	claimArrived := make(chan struct{}, 2)
	claimRelease := make(chan struct{})
	var releaseClaimsOnce sync.Once
	releaseClaims := func() { releaseClaimsOnce.Do(func() { close(claimRelease) }) }
	t.Cleanup(releaseClaims)
	effects := &fakeSourceProcessEffects{
		credentials:  peerCredentials{PID: 300, UID: 1001, GID: 1002},
		claimArrived: claimArrived,
		claimRelease: claimRelease,
		identities: map[int][]processIdentity{
			300: {{PID: 300, ParentPID: 100, StartTime: 30}},
			100: {{PID: 100, ParentPID: 1, StartTime: 10}},
		},
	}
	binding := testPublishedBinding(
		"00112233445566778899aabbccddeeff",
		sourceAccount{UID: 1001, GID: 1002},
		processIdentity{PID: 100, ParentPID: 1, StartTime: 10},
		&fakePinnedProcess{},
	)
	agent := testSourceAgent(effects, binding)
	first, firstPeer := net.Pipe()
	second, secondPeer := net.Pipe()
	t.Cleanup(func() { _ = first.Close(); _ = firstPeer.Close() })
	t.Cleanup(func() { _ = second.Close(); _ = secondPeer.Close() })
	results := make(chan error, 2)
	go func() { _, err := agent.claimSourceForPeer(t.Context(), first); results <- err }()
	go func() { _, err := agent.claimSourceForPeer(t.Context(), second); results <- err }()
	for range 2 {
		select {
		case <-claimArrived:
		case <-time.After(time.Second):
			t.Fatal("concurrent source claims did not both reach the admission barrier")
		}
	}
	releaseClaims()
	claimErrors := make([]error, 0, 2)
	for range 2 {
		select {
		case claimErr := <-results:
			claimErrors = append(claimErrors, claimErr)
		case <-time.After(time.Second):
			t.Fatal("concurrent source claim did not finish")
		}
	}
	winners := 0
	for _, claimErr := range claimErrors {
		if claimErr == nil {
			winners++
		}
	}
	assert.Equal(t, 1, winners)
	agent.cleanupSourceBinding(binding)
	agent.cleanupSourceBinding(binding)
	effects.mu.Lock()
	defer effects.mu.Unlock()
	for _, pin := range effects.peerPins {
		pin.mu.Lock()
		assert.Equal(t, 1, pin.closes)
		pin.mu.Unlock()
	}
}

func TestUnrelatedRootPeerWithSameCredentialsIsRejected(t *testing.T) {
	effects := &fakeSourceProcessEffects{
		credentials: peerCredentials{PID: 300, UID: 0, GID: 0},
		identities: map[int][]processIdentity{
			300: {{PID: 300, ParentPID: 50, StartTime: 30}},
			50:  {{PID: 50, ParentPID: 1, StartTime: 5}},
		},
	}
	binding := testPublishedBinding(
		"00112233445566778899aabbccddeeff",
		sourceAccount{UID: 0, GID: 0},
		processIdentity{PID: 100, ParentPID: 1, StartTime: 10},
		&fakePinnedProcess{},
	)
	agent := testSourceAgent(effects, binding)
	local, peer := net.Pipe()
	t.Cleanup(func() { _ = local.Close(); _ = peer.Close() })
	_, err := agent.claimSourceForPeer(t.Context(), local)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a descendant")
}

func TestSourceClaimRejectsStablePeerPIDReuseMismatch(t *testing.T) {
	effects := &fakeSourceProcessEffects{
		credentials: peerCredentials{PID: 300, UID: 1001, GID: 1002},
		identities: map[int][]processIdentity{
			300: {
				{PID: 300, ParentPID: 100, StartTime: 30},
				{PID: 300, ParentPID: 100, StartTime: 31},
			},
		},
	}
	binding := testPublishedBinding(
		"00112233445566778899aabbccddeeff",
		sourceAccount{UID: 1001, GID: 1002},
		processIdentity{PID: 100, ParentPID: 1, StartTime: 10},
		&fakePinnedProcess{},
	)
	agent := testSourceAgent(effects, binding)
	local, peer := net.Pipe()
	t.Cleanup(func() { _ = local.Close() })
	t.Cleanup(func() { _ = peer.Close() })
	_, err := agent.claimSourceForPeer(t.Context(), local)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "changed during admission")
}

func TestSourceClaimRejectsStableReusedRootPIDMismatchAndCleansPins(t *testing.T) {
	effects := &fakeSourceProcessEffects{
		credentials: peerCredentials{PID: 300, UID: 1001, GID: 1002},
		identities: map[int][]processIdentity{
			300: {{PID: 300, ParentPID: 100, StartTime: 30}},
			100: {{PID: 100, ParentPID: 1, StartTime: 11}},
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

	_, err := agent.claimSourceForPeer(t.Context(), local)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a descendant")
	agent.cleanupSourceBinding(binding)

	effects.mu.Lock()
	require.Len(t, effects.peerPins, 1)
	peerPin := effects.peerPins[0]
	effects.mu.Unlock()
	peerPin.mu.Lock()
	assert.Equal(t, 1, peerPin.closes)
	peerPin.mu.Unlock()
	rootPin.mu.Lock()
	assert.Equal(t, 1, rootPin.closes)
	rootPin.mu.Unlock()
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
	assert.Empty(t, cmp.Diff(request, forwarded))

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
	assert.Empty(t, cmp.Diff(wantBinary, stdout.Data))
	_, err = reader.Read(t.Context())
	require.NoError(t, err)
	_, err = reader.Read(t.Context())
	assert.ErrorIs(t, err, io.EOF)
	assert.NoError(t, <-responseDone)
	_ = localCLI.Close()
	<-handlerDone
}

func TestSharedAgentBindingsAttachInReverseOrderWithoutCrossWire(t *testing.T) {
	firstLocalAgent, firstCLI := net.Pipe()
	secondLocalAgent, secondCLI := net.Pipe()
	firstSourceAgent, firstHost := net.Pipe()
	secondSourceAgent, secondHost := net.Pipe()
	t.Cleanup(func() { _ = firstCLI.Close() })
	t.Cleanup(func() { _ = secondCLI.Close() })
	t.Cleanup(func() { _ = firstHost.Close() })
	t.Cleanup(func() { _ = secondHost.Close() })
	deadline := time.Now().Add(time.Second)
	for _, conn := range []net.Conn{firstCLI, secondCLI, firstHost, secondHost} {
		require.NoError(t, conn.SetDeadline(deadline))
	}
	effects := &fakeSourceProcessEffects{
		credentialFunc: func(conn net.Conn) peerCredentials {
			if conn == firstLocalAgent {
				return peerCredentials{PID: 301, UID: 1001, GID: 1002}
			}
			return peerCredentials{PID: 302, UID: 1001, GID: 1002}
		},
		identities: map[int][]processIdentity{
			301: {{PID: 301, ParentPID: 101, StartTime: 31}},
			101: {{PID: 101, ParentPID: 1, StartTime: 11}},
			302: {{PID: 302, ParentPID: 102, StartTime: 32}},
			102: {{PID: 102, ParentPID: 1, StartTime: 12}},
		},
	}
	firstBinding := testPublishedBinding(
		"00112233445566778899aabbccddeeff",
		sourceAccount{UID: 1001, GID: 1002},
		processIdentity{PID: 101, ParentPID: 1, StartTime: 11},
		&fakePinnedProcess{},
	)
	firstBinding.transport = newSourceTransport(firstSourceAgent)
	secondBinding := testPublishedBinding(
		"ffeeddccbbaa99887766554433221100",
		sourceAccount{UID: 1001, GID: 1002},
		processIdentity{PID: 102, ParentPID: 1, StartTime: 12},
		&fakePinnedProcess{},
	)
	secondBinding.transport = newSourceTransport(secondSourceAgent)
	agent := testSourceAgent(effects, firstBinding, secondBinding)
	firstDone := make(chan struct{})
	secondDone := make(chan struct{})
	go func() { agent.handleLocalConn(context.Background(), secondLocalAgent); close(secondDone) }()
	go func() { agent.handleLocalConn(context.Background(), firstLocalAgent); close(firstDone) }()

	secondRequest := vmexecwire.SourceRequest{
		RequestID: "ffeeddccbbaa99887766554433221100", Destination: "second", TargetUser: "alice", Command: "true",
	}
	firstRequest := vmexecwire.SourceRequest{
		RequestID: "00112233445566778899aabbccddeeff", Destination: "first", TargetUser: "alice", Command: "true",
	}
	require.NoError(t, vmexecwire.EncodeSourceRequest(t.Context(), secondCLI, secondRequest))
	forwardedSecond, err := vmexecwire.DecodeSourceRequest(t.Context(), secondHost)
	require.NoError(t, err)
	assert.Empty(t, cmp.Diff(secondRequest, forwardedSecond))
	require.NoError(t, vmexecwire.EncodeSourceRequest(t.Context(), firstCLI, firstRequest))
	forwardedFirst, err := vmexecwire.DecodeSourceRequest(t.Context(), firstHost)
	require.NoError(t, err)
	assert.Empty(t, cmp.Diff(firstRequest, forwardedFirst))

	require.NoError(t, secondCLI.Close())
	require.NoError(t, firstCLI.Close())
	select {
	case <-secondDone:
	case <-time.After(time.Second):
		t.Fatal("second reverse-order relay did not stop")
	}
	select {
	case <-firstDone:
	case <-time.After(time.Second):
		t.Fatal("first reverse-order relay did not stop")
	}
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
	pidFD := -1
	configureSourceCommand(command, &pidFD)
	require.NotNil(t, command.SysProcAttr)
	assert.True(t, command.SysProcAttr.Setsid)
	assert.Same(t, &pidFD, command.SysProcAttr.PidFD)
	assert.Equal(t, 2*time.Second, command.WaitDelay)
}

func TestSourceCommandCancellationKillsDescendantHoldingOutputPipe(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	command := exec.CommandContext(ctx, "sh", "-c", "echo ready; sleep 30 & wait")
	started := make(chan struct{})
	command.Stdout = &signalWriter{written: started}
	pidFD := -1
	configureSourceCommand(command, &pidFD)
	require.NoError(t, command.Start())
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("source command did not publish its descendant barrier")
	}
	cancel()
	waited := make(chan error, 1)
	go func() { waited <- command.Wait() }()
	select {
	case err := <-waited:
		require.Error(t, err)
	case <-time.After(3 * time.Second):
		t.Fatal("source command cancellation left a descendant holding output pipes")
	}
	if pidFD >= 0 {
		assert.NoError(t, unix.Close(pidFD))
	}
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

func TestRootOuterSourceSessionIsAcceptedAndBoundToRootCredentials(t *testing.T) {
	effects := &fakeSourceProcessEffects{account: sourceAccount{UID: 0, GID: 0}}
	agent := testSourceAgent(effects)
	transport := newSourceTransport(&discardConn{})
	agent.pendingSources["00112233445566778899aabbccddeeff"] = transport

	binding, err := agent.reserveSourceSession(
		t.Context(), "00112233445566778899aabbccddeeff", "",
	)
	require.NoError(t, err)
	assert.Empty(t, cmp.Diff(sourceAccount{UID: 0, GID: 0}, binding.account))
	effects.mu.Lock()
	assert.Equal(t, 1, effects.accountCalls)
	assert.Empty(t, cmp.Diff([]string{"root"}, effects.accountNames))
	effects.mu.Unlock()
	agent.cleanupSourceBinding(binding)
}

func TestUnauthenticatedPartialLocalConnectionIsRejectedBeforeRequestRead(t *testing.T) {
	effects := &fakeSourceProcessEffects{credentialErr: errors.New("unauthorized peer")}
	agent := testSourceAgent(effects)
	conn := &readCountingConn{}

	agent.handleLocalConn(t.Context(), conn)
	assert.Zero(t, conn.readCalls)
	assert.True(t, conn.closed)
}

func TestSourceAdmissionHasFixedConcurrencyCap(t *testing.T) {
	agent := testSourceAgent(&fakeSourceProcessEffects{})
	for range maxSourceAdmissions {
		assert.True(t, agent.beginSourceAdmission())
	}
	assert.False(t, agent.beginSourceAdmission())
	for range maxSourceAdmissions {
		<-agent.sourceAdmission
	}
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

func waitForSourceClaim(t *testing.T, agent *Agent, binding *sourceBinding) {
	t.Helper()
	watchdog := time.NewTimer(time.Second)
	defer watchdog.Stop()
	for {
		agent.sourceMu.Lock()
		claimed := binding.claimed
		changed := agent.sourceChanged
		agent.sourceMu.Unlock()
		if claimed {
			return
		}
		select {
		case <-changed:
		case <-watchdog.C:
			t.Fatal("source binding was not claimed")
		}
	}
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

type readCountingConn struct {
	readCalls int
	closed    bool
}

func (conn *readCountingConn) Read([]byte) (int, error) {
	conn.readCalls++
	return 0, io.ErrUnexpectedEOF
}

func (*readCountingConn) Write(value []byte) (int, error) { return len(value), nil }
func (conn *readCountingConn) Close() error {
	conn.closed = true
	return nil
}
func (*readCountingConn) LocalAddr() net.Addr              { return &net.UnixAddr{} }
func (*readCountingConn) RemoteAddr() net.Addr             { return &net.UnixAddr{} }
func (*readCountingConn) SetDeadline(time.Time) error      { return nil }
func (*readCountingConn) SetReadDeadline(time.Time) error  { return nil }
func (*readCountingConn) SetWriteDeadline(time.Time) error { return nil }

type signalWriter struct {
	once    sync.Once
	written chan struct{}
}

func (writer *signalWriter) Write(value []byte) (int, error) {
	writer.once.Do(func() { close(writer.written) })
	return len(value), nil
}
