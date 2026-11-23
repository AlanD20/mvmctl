package guestcontrolauth

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"io"
	"net"
	"time"
)

const protocol = "mvm-guest-control/1"

// acceptance is a TLS-protected authentication acknowledgement, not feature authorization or worker readiness.
const acceptance = "MVMGAU01"

// AuthenticateAgent authenticates the agent through an already VMM-admitted connection.
// It consumes connection ownership on failure and transfers the TLS connection on success. The caller must
// provide a deadline; cancellation interrupts TLS AND acceptance I/O. The caller owns session cancellation
// after return. No VM identity, delegation or network authority is established by this function.
func AuthenticateAgent(ctx context.Context, conn net.Conn, value RootHostMaterial) (*tls.Conn, error) {
	return authenticate(ctx, conn, value.value, true)
}

// AuthenticateHost accepts only the pinned host credential on the dedicated Guest Control listener.
// It follows the same connection-ownership and bounded-context contract as AuthenticateAgent.
func AuthenticateHost(ctx context.Context, conn net.Conn, value AgentMaterial) (*tls.Conn, error) {
	return authenticate(ctx, conn, value.value, false)
}

func authenticatedConfig(value *material) *tls.Config {
	return &tls.Config{
		MinVersion: tls.VersionTLS13, MaxVersion: tls.VersionTLS13,
		Certificates: []tls.Certificate{value.pair}, NextProtos: []string{protocol},
		ClientAuth: tls.RequireAnyClientCert, SessionTicketsDisabled: true,
		// CRITICAL: Verification is exclusively the provisioned exact certificate pin below.
		// There is no PKI, hostname, subject identity or wall-clock trust fallback. In particular a
		// memory-snapshot guest clock cannot revoke or revive a provisioned key binding. Credential
		// retirement and lineage references belong to root authority outside this leaf package.
		InsecureSkipVerify: true,
		VerifyConnection: func(state tls.ConnectionState) error {
			if state.Version != tls.VersionTLS13 || state.NegotiatedProtocol != protocol || state.DidResume ||
				len(state.PeerCertificates) != 1 {
				return invalid("TLS protocol or peer certificate is invalid")
			}
			observed := sha256.Sum256(state.PeerCertificates[0].Raw)
			if subtle.ConstantTimeCompare(value.pin[:], observed[:]) != 1 {
				return invalid("TLS certificate pin differs")
			}
			return nil
		},
	}
}

func authenticate(ctx context.Context, conn net.Conn, value *material, host bool) (_ *tls.Conn, returnErr error) {
	accepted := false
	defer func() {
		if !accepted && conn != nil {
			_ = conn.Close()
		}
	}()
	if ctx == nil || conn == nil || value == nil {
		return nil, invalid("authentication context, connection and material are required")
	}
	deadline, bounded := ctx.Deadline()
	if !bounded {
		return nil, invalid("authentication context requires a deadline")
	}
	if err := ctx.Err(); err != nil {
		return nil, failed("authentication cancelled", err)
	}
	if err := conn.SetDeadline(deadline); err != nil {
		return nil, failed("authentication deadline is unavailable", nil)
	}
	done := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { _ = conn.Close(); close(done) })
	defer stop()
	config := authenticatedConfig(value)
	var secured *tls.Conn
	if host {
		secured = tls.Client(conn, config)
	} else {
		secured = tls.Server(conn, config)
	}
	if err := secured.HandshakeContext(ctx); err != nil {
		return nil, failed("TLS authentication failed", ctx.Err())
	}
	// TLS 1.3 clients can finish before the server accepts their certificate. The server writes
	// this fixed acknowledgement only after verifying the host, so the host cannot report early success.
	if host {
		var observed [len(acceptance)]byte
		if _, err := io.ReadFull(secured, observed[:]); err != nil || string(observed[:]) != acceptance {
			return nil, failed("TLS authentication acceptance failed", ctx.Err())
		}
	} else {
		if written, err := io.WriteString(secured, acceptance); err != nil || written != len(acceptance) {
			return nil, failed("TLS authentication acceptance failed", ctx.Err())
		}
	}
	if !stop() {
		<-done
		return nil, failed("authentication cancelled", ctx.Err())
	}
	if err := ctx.Err(); err != nil {
		return nil, failed("authentication cancelled", err)
	}
	if err := conn.SetDeadline(time.Time{}); err != nil {
		return nil, failed("authentication deadline cleanup failed", nil)
	}
	accepted = true
	return secured, nil
}
