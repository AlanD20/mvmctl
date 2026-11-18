// Package guestcontrolauth authenticates provisioned Guest Control endpoints with pinned mutual TLS.
// Credentials prove key possession, not a VM, UID, delegation, or uncompromised guest. Root must derive
// the session principal from the protected VMM peer and current launch authority separately.
package guestcontrolauth

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"math/big"
	"time"

	"mvmctl/pkg/errs"
)

const materialVersion = 1

// RootHostMaterial is immutable host-private material and the exact pinned agent certificate.
// Decode it only from root-controlled authority; structural validation does not establish provenance.
type RootHostMaterial struct{ value *material }

// AgentMaterial contains only the agent's private key and the pinned public host certificate.
type AgentMaterial struct{ value *material }

type material struct {
	pair       tls.Certificate
	privateDER []byte
	peerDER    []byte
	pin        [sha256.Size]byte
}

// materialRecord is versioned and canonical for later descriptor-pinned persistence. This package
// performs no filesystem I/O. The host and agent records have deliberately different roles.
type materialRecord struct {
	Version         uint32 `json:"version"`
	Role            string `json:"role"`
	Certificate     []byte `json:"certificate_der"`
	PrivateKey      []byte `json:"private_key_pkcs8"`
	PeerCertificate []byte `json:"peer_certificate_der"`
}

// Generate creates independent Ed25519 pairs. The agent material never receives the host private key.
func Generate(ctx context.Context) (RootHostMaterial, AgentMaterial, error) {
	if ctx == nil {
		return RootHostMaterial{}, AgentMaterial{}, invalid("generation context is required")
	}
	if err := ctx.Err(); err != nil {
		return RootHostMaterial{}, AgentMaterial{}, failed("generation cancelled", err)
	}
	hostCert, hostKey, err := generatePair(x509.ExtKeyUsageClientAuth)
	if err != nil {
		return RootHostMaterial{}, AgentMaterial{}, err
	}
	agentCert, agentKey, err := generatePair(x509.ExtKeyUsageServerAuth)
	if err != nil {
		return RootHostMaterial{}, AgentMaterial{}, err
	}
	host, err := parseMaterial(materialRecord{materialVersion, "host", hostCert, hostKey, agentCert}, "host")
	if err != nil {
		return RootHostMaterial{}, AgentMaterial{}, err
	}
	agent, err := parseMaterial(materialRecord{materialVersion, "agent", agentCert, agentKey, hostCert}, "agent")
	if err != nil {
		return RootHostMaterial{}, AgentMaterial{}, err
	}
	if err := ctx.Err(); err != nil {
		return RootHostMaterial{}, AgentMaterial{}, failed("generation cancelled", err)
	}
	return RootHostMaterial{host}, AgentMaterial{agent}, nil
}

func generatePair(usage x509.ExtKeyUsage) ([]byte, []byte, error) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, failed("generate credential key", nil)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, nil, failed("generate credential serial", nil)
	}
	serial.Add(serial, big.NewInt(1))
	// These are pinned key containers, not expiring PKI identities. Verification deliberately does
	// not consult either machine's clock, including the guest clock restored from a memory snapshot.
	template := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "mvmctl Guest Control"},
		NotBefore: time.Date(1970, 1, 1, 0, 0, 0, 0, time.UTC),
		NotAfter:  time.Date(9999, 12, 31, 23, 59, 59, 0, time.UTC),
		KeyUsage:  x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{usage}, BasicConstraintsValid: true}
	cert, err := x509.CreateCertificate(rand.Reader, template, template, public, private)
	if err != nil {
		return nil, nil, failed("generate credential certificate", nil)
	}
	key, err := x509.MarshalPKCS8PrivateKey(private)
	if err != nil {
		return nil, nil, failed("encode credential key", nil)
	}
	return cert, key, nil
}

func credentialCertificate(raw []byte, usage x509.ExtKeyUsage) (*x509.Certificate, error) {
	cert, err := x509.ParseCertificate(raw)
	if err != nil {
		return nil, invalid("credential certificate is invalid")
	}
	_, ed := cert.PublicKey.(ed25519.PublicKey)
	if !ed || cert.SignatureAlgorithm != x509.PureEd25519 || cert.IsCA ||
		cert.KeyUsage != x509.KeyUsageDigitalSignature || len(cert.ExtKeyUsage) != 1 ||
		cert.ExtKeyUsage[0] != usage || len(cert.UnknownExtKeyUsage) != 0 ||
		len(cert.UnhandledCriticalExtensions) != 0 ||
		cert.CheckSignature(cert.SignatureAlgorithm, cert.RawTBSCertificate, cert.Signature) != nil {
		return nil, invalid("credential certificate algorithm, role or signature is invalid")
	}
	return cert, nil
}

func parseMaterial(record materialRecord, role string) (*material, error) {
	if record.Version != materialVersion || record.Role != role {
		return nil, invalid("credential version or role is invalid")
	}
	ownUsage, peerUsage := x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth
	if role == "agent" {
		ownUsage, peerUsage = peerUsage, ownUsage
	}
	cert, err := credentialCertificate(record.Certificate, ownUsage)
	if err != nil {
		return nil, err
	}
	if _, err := credentialCertificate(record.PeerCertificate, peerUsage); err != nil {
		return nil, err
	}
	parsed, err := x509.ParsePKCS8PrivateKey(record.PrivateKey)
	if err != nil {
		return nil, invalid("credential private key is invalid")
	}
	private, ok := parsed.(ed25519.PrivateKey)
	if !ok || len(private) != ed25519.PrivateKeySize {
		return nil, invalid("credential private key algorithm is invalid")
	}
	// Re-derive the entire key: Ed25519's encoded public suffix alone is not proof of a valid pair.
	derived := ed25519.NewKeyFromSeed(private.Seed())
	if subtle.ConstantTimeCompare(private, derived) != 1 ||
		subtle.ConstantTimeCompare(derived.Public().(ed25519.PublicKey), cert.PublicKey.(ed25519.PublicKey)) != 1 {
		return nil, invalid("credential private key and certificate differ")
	}
	return &material{pair: tls.Certificate{Certificate: [][]byte{bytes.Clone(record.Certificate)},
		PrivateKey: ed25519.PrivateKey(bytes.Clone(derived)), Leaf: cert}, privateDER: bytes.Clone(record.PrivateKey),
		peerDER: bytes.Clone(record.PeerCertificate), pin: sha256.Sum256(record.PeerCertificate)}, nil
}

// Encode returns a fresh canonical secret record; callers own its storage and access control.
func (value RootHostMaterial) Encode() ([]byte, error) { return encodeMaterial(value.value, "host") }

// Encode returns only agent-private material and the host public pin.
func (value AgentMaterial) Encode() ([]byte, error) { return encodeMaterial(value.value, "agent") }

func encodeMaterial(value *material, role string) ([]byte, error) {
	if value == nil {
		return nil, invalid("credential material is unavailable")
	}
	record := materialRecord{materialVersion, role, value.pair.Certificate[0], value.privateDER, value.peerDER}
	raw, err := json.Marshal(record)
	if err != nil {
		return nil, invalid("credential record encoding failed")
	}
	return raw, nil
}

// DecodeRootHostMaterial validates a canonical root-authority record, never a caller database token.
func DecodeRootHostMaterial(raw []byte) (RootHostMaterial, error) {
	value, err := decodeMaterial(raw, "host")
	return RootHostMaterial{value}, err
}

// DecodeAgentMaterial validates the separately provisioned agent record.
func DecodeAgentMaterial(raw []byte) (AgentMaterial, error) {
	value, err := decodeMaterial(raw, "agent")
	return AgentMaterial{value}, err
}

func decodeMaterial(raw []byte, role string) (*material, error) {
	var record materialRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		return nil, invalid("credential record is invalid")
	}
	canonical, err := json.Marshal(record)
	if err != nil || !bytes.Equal(canonical, raw) {
		return nil, invalid("credential record is not canonical")
	}
	return parseMaterial(record, role)
}

func invalid(message string) error {
	return errs.New(errs.CodeValidationFailed, "Guest Control "+message)
}
func failed(message string, cause error) error {
	return errs.WrapMsg(errs.CodeVsockHandshakeFailed, "Guest Control "+message, cause)
}
