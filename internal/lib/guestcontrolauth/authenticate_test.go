package guestcontrolauth

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"io"
	"math/big"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"mvmctl/pkg/errs"
)

type authResult struct {
	conn *tls.Conn
	err  error
}

func testMaterial(t *testing.T) (RootHostMaterial, AgentMaterial) {
	t.Helper()
	host, agent, err := Generate(t.Context())
	require.NoError(t, err)
	return host, agent
}

func authPair(t *testing.T, host RootHostMaterial, agent AgentMaterial) (authResult, authResult) {
	t.Helper()
	left, right := net.Pipe()
	t.Cleanup(func() { _ = left.Close(); _ = right.Close() })
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	server := make(chan authResult, 1)
	go func() { conn, err := AuthenticateHost(ctx, right, agent); server <- authResult{conn, err} }()
	conn, err := AuthenticateAgent(ctx, left, host)
	return authResult{conn, err}, <-server
}

func TestAuthenticationRoundTripUsesTLS13AndPreservesApplicationStream(t *testing.T) {
	host, agent := testMaterial(t)
	hostRaw, err := host.Encode()
	require.NoError(t, err)
	agentRaw, err := agent.Encode()
	require.NoError(t, err)
	host, err = DecodeRootHostMaterial(hostRaw)
	require.NoError(t, err)
	agent, err = DecodeAgentMaterial(agentRaw)
	require.NoError(t, err)
	// Mutating serialized copies cannot modify the opaque material retained for authentication.
	clear(hostRaw)
	clear(agentRaw)
	client, server := authPair(t, host, agent)
	require.NoError(t, client.err)
	require.NoError(t, server.err)
	require.Equal(t, uint16(tls.VersionTLS13), client.conn.ConnectionState().Version)
	require.Equal(t, protocol, server.conn.ConnectionState().NegotiatedProtocol)
	write := make(chan error, 1)
	go func() { _, err := server.conn.Write([]byte("feature-frame")); write <- err }()
	var payload [13]byte
	_, err = io.ReadFull(client.conn, payload[:])
	require.NoError(t, err)
	require.Equal(t, "feature-frame", string(payload[:]))
	require.NoError(t, <-write)
}

func TestAuthenticationUsesPinnedKeyBindingsInsteadOfSnapshotClockValidity(t *testing.T) {
	host, agent := testMaterial(t)
	// Produce genuinely expired DER certificates, not a mutation of parsed TLS state.
	for _, value := range []*material{host.value, agent.value} {
		original := value.pair.Leaf
		template := *original
		template.SerialNumber = big.NewInt(42)
		template.NotBefore = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
		template.NotAfter = time.Date(2000, 1, 2, 0, 0, 0, 0, time.UTC)
		der, err := x509.CreateCertificate(rand.Reader, &template, &template, original.PublicKey, value.pair.PrivateKey)
		require.NoError(t, err)
		value.pair.Certificate = [][]byte{der}
		value.pair.Leaf, err = x509.ParseCertificate(der)
		require.NoError(t, err)
	}
	host.value.peerDER = agent.value.pair.Certificate[0]
	agent.value.peerDER = host.value.pair.Certificate[0]
	hostRaw, err := host.Encode()
	require.NoError(t, err)
	agentRaw, err := agent.Encode()
	require.NoError(t, err)
	host, err = DecodeRootHostMaterial(hostRaw)
	require.NoError(t, err)
	agent, err = DecodeAgentMaterial(agentRaw)
	require.NoError(t, err)
	client, server := authPair(t, host, agent)
	require.NoError(t, client.err)
	require.NoError(t, server.err)
}

func TestAuthenticationRejectsEitherWrongPinWithoutReturningCapability(t *testing.T) {
	host, agent := testMaterial(t)
	otherHost, otherAgent := testMaterial(t)
	// Isolate wrong agent pin: the other agent still accepts the correct host credential.
	otherAgent.value.peerDER = bytes.Clone(agent.value.peerDER)
	otherAgent.value.pin = agent.value.pin
	tests := map[string]struct {
		host  RootHostMaterial
		agent AgentMaterial
	}{
		"agent_pin":                       {host, otherAgent},
		"host_pin_after_client_handshake": {otherHost, agent},
	}
	// Isolate wrong host pin: retain the expected agent pin, change only host credentials.
	tests["host_pin_after_client_handshake"].host.value.peerDER = bytes.Clone(host.value.peerDER)
	tests["host_pin_after_client_handshake"].host.value.pin = host.value.pin
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			client, server := authPair(t, test.host, test.agent)
			require.Error(t, client.err)
			require.Nil(t, client.conn)
			require.Error(t, server.err)
			require.Nil(t, server.conn)
			require.NotNil(t, errs.AsDomainError(client.err))
		})
	}
}

func TestAuthenticationRejectsWrongRoleALPNAndProtocol(t *testing.T) {
	host, agent := testMaterial(t)
	for _, name := range []string{"wrong_role", "wrong_ALPN", "missing_ALPN", "TLS12"} {
		t.Run(name, func(t *testing.T) {
			left, right := net.Pipe()
			defer left.Close()
			defer right.Close()
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			config := authenticatedConfig(host.value)
			// This controlled client deliberately accepts the server; only the public server API
			// must reject the bad role/protocol. A second client-side refusal would mask regressions.
			config.VerifyConnection = nil
			switch name {
			case "wrong_role":
				config.Certificates = []tls.Certificate{agent.value.pair}
			case "wrong_ALPN":
				config.NextProtos = []string{"another-protocol"}
			case "missing_ALPN":
				config.NextProtos = nil
			case "TLS12":
				config.MinVersion, config.MaxVersion = tls.VersionTLS12, tls.VersionTLS12
			}
			finished := make(chan authResult, 1)
			go func() { conn, err := AuthenticateHost(ctx, right, agent); finished <- authResult{conn, err} }()
			client := tls.Client(left, config)
			if client.HandshakeContext(ctx) == nil {
				var acknowledged [len(acceptance)]byte
				_, _ = io.ReadFull(client, acknowledged[:])
			}
			_ = left.Close()
			result := <-finished
			require.Error(t, result.err)
			require.Nil(t, result.conn)
		})
	}
}

func TestAuthenticationAcceptanceRejectsWrongOrTruncatedAndPreservesFollowingBytes(t *testing.T) {
	host, agent := testMaterial(t)
	for _, text := range []string{"MVMGAU02", "MVMG", acceptance + "next"} {
		t.Run(text, func(t *testing.T) {
			left, right := net.Pipe()
			defer left.Close()
			defer right.Close()
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			finished := make(chan struct{})
			go func() {
				defer close(finished)
				secured := tls.Server(right, authenticatedConfig(agent.value))
				if secured.HandshakeContext(ctx) == nil {
					_, _ = io.WriteString(secured, text)
				}
				_ = right.Close()
			}()
			secured, err := AuthenticateAgent(ctx, left, host)
			if text == acceptance+"next" {
				require.NoError(t, err)
				var next [4]byte
				_, err := io.ReadFull(secured, next[:])
				require.NoError(t, err)
				require.Equal(t, "next", string(next[:]))
			} else {
				require.Error(t, err)
				require.Nil(t, secured)
			}
			<-finished
		})
	}
}

type signallingConn struct {
	net.Conn
	once    sync.Once
	entered chan struct{}
}

func (conn *signallingConn) Write(raw []byte) (int, error) {
	conn.once.Do(func() { close(conn.entered) })
	return conn.Conn.Write(raw)
}

func TestAuthenticationCancellationInterruptsBlockedTLSAndAcceptance(t *testing.T) {
	host, agent := testMaterial(t)
	for _, duringAck := range []bool{false, true} {
		name := "TLS"
		if duringAck {
			name = "acceptance"
		}
		t.Run(name, func(t *testing.T) {
			left, right := net.Pipe()
			defer right.Close()
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			entered := make(chan struct{})
			var connection net.Conn = &signallingConn{Conn: left, entered: entered}
			serverDone := make(chan struct{})
			if duringAck {
				connection = left
				go func() {
					defer close(serverDone)
					_ = tls.Server(right, authenticatedConfig(agent.value)).HandshakeContext(ctx)
					close(entered)
					<-ctx.Done()
					_ = right.Close()
				}()
			} else {
				close(serverDone)
			}
			finished := make(chan authResult, 1)
			go func() { conn, err := AuthenticateAgent(ctx, connection, host); finished <- authResult{conn, err} }()
			<-entered
			cancel()
			result := <-finished
			require.ErrorIs(t, result.err, context.Canceled)
			require.Nil(t, result.conn)
			<-serverDone
		})
	}
}

func TestAuthenticationRequiresBoundedContextAndMaterial(t *testing.T) {
	host, _ := testMaterial(t)
	for _, ctx := range []context.Context{nil, context.Background()} {
		left, right := net.Pipe()
		conn, err := AuthenticateAgent(ctx, left, host)
		require.Error(t, err)
		require.Nil(t, conn)
		_, err = right.Write([]byte{1})
		require.Error(t, err, "refused authentication must close the consumed connection")
		_ = right.Close()
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	left, right := net.Pipe()
	defer right.Close()
	conn, err := AuthenticateAgent(ctx, left, RootHostMaterial{})
	require.Error(t, err)
	require.Nil(t, conn)
}

func TestAuthenticationNeverResumesWithTicketOfferingPeer(t *testing.T) {
	host, agent := testMaterial(t)
	config := authenticatedConfig(agent.value)
	config.SessionTicketsDisabled = false // A real peer offers tickets; the public host API must not cache/reuse them.
	for range 2 {
		left, right := net.Pipe()
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		server := make(chan error, 1)
		go func() {
			secured := tls.Server(right, config)
			if err := secured.HandshakeContext(ctx); err != nil {
				server <- err
				return
			}
			_, err := io.WriteString(secured, acceptance)
			server <- err
		}()
		secured, err := AuthenticateAgent(ctx, left, host)
		require.NoError(t, err)
		require.False(t, secured.ConnectionState().DidResume)
		require.NoError(t, <-server)
		_ = left.Close()
		_ = right.Close()
		cancel()
	}
}

func TestMaterialValidationRejectsRoleKeyAndNoncanonicalRecords(t *testing.T) {
	host, agent := testMaterial(t)
	raw, err := host.Encode()
	require.NoError(t, err)
	var original materialRecord
	require.NoError(t, json.Unmarshal(raw, &original))
	agentRaw, err := agent.Encode()
	require.NoError(t, err)
	var guest materialRecord
	require.NoError(t, json.Unmarshal(agentRaw, &guest))
	require.NotEqual(t, original.PrivateKey, guest.PrivateKey)
	require.Equal(t, original.Certificate, guest.PeerCertificate)
	cases := map[string]func(*materialRecord){
		"version":           func(r *materialRecord) { r.Version++ },
		"role":              func(r *materialRecord) { r.Role = "agent" },
		"wrong_key":         func(r *materialRecord) { r.PrivateKey = guest.PrivateKey },
		"empty_pin":         func(r *materialRecord) { r.PeerCertificate = nil },
		"wrong_peer_role":   func(r *materialRecord) { r.PeerCertificate = r.Certificate },
		"damaged_signature": func(r *materialRecord) { r.Certificate[len(r.Certificate)-1] ^= 1 },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			record := original
			record.Certificate = bytes.Clone(original.Certificate)
			mutate(&record)
			raw, err := json.Marshal(record)
			require.NoError(t, err)
			_, err = DecodeRootHostMaterial(raw)
			require.Error(t, err)
			require.NotContains(t, err.Error(), string(raw))
		})
	}
	for _, malformed := range [][]byte{append(raw, '\n'), bytes.Replace(raw, []byte(`"version":1`),
		[]byte(`"version":1,"version":1`), 1), agentRaw} {
		_, err := DecodeRootHostMaterial(malformed)
		require.Error(t, err)
	}
}
