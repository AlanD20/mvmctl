package key

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/pkg/errs"
)

func TestComputeFingerprint(t *testing.T) {
	t.Run("valid_rsa_key", func(t *testing.T) {
		pubKey := "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABAQDA== test@host"
		fp, err := computeFingerprint(pubKey)
		require.NoError(t, err)
		assert.True(t, strings.HasPrefix(fp, "SHA256:"),
			"expected SHA256: prefix, got %s", fp)
		assert.Len(t, fp, 50)
	})

	t.Run("invalid_format_missing_key", func(t *testing.T) {
		_, err := computeFingerprint("ssh-rsa")
		require.Error(t, err)
		assertCode(t, err, errs.CodeKeyError)
	})

	t.Run("invalid_format_not_base64", func(t *testing.T) {
		_, err := computeFingerprint("ssh-rsa !!!not-base64!!!")
		require.Error(t, err)
		assertCode(t, err, errs.CodeKeyError)
	})

	t.Run("ed25519_key", func(t *testing.T) {
		pubKey := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIKQ= user@host"
		fp, err := computeFingerprint(pubKey)
		require.NoError(t, err)
		assert.True(t, strings.HasPrefix(fp, "SHA256:"),
			"expected SHA256: prefix, got %s", fp)
	})
}

func TestIsPrivateKey(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    bool
	}{
		{"rsa_private", "-----BEGIN RSA PRIVATE KEY-----\nMIICWwIBAA==\n-----END RSA PRIVATE KEY-----", true},
		{"ec_private", "-----BEGIN EC PRIVATE KEY-----\nMHQCAQEE==\n-----END EC PRIVATE KEY-----", true},
		{"openssh_private", "-----BEGIN OPENSSH PRIVATE KEY-----\nb3BlbnNz==\n-----END OPENSSH PRIVATE KEY-----", true},
		{"public_key", "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABAQDA== test@host", false},
		{"empty", "", false},
		{"random_text", "hello world", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsPrivateKey(tt.content)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestParseAlgorithm(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
		wantErr bool
	}{
		{"rsa", "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABAQDA== test@host", "ssh-rsa", false},
		{"ed25519", "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIKQ= user@host", "ssh-ed25519", false},
		{"empty", "", "", true},
		{"only_spaces", "   ", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseAlgorithm(tt.content)
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestParseComment(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{"with_comment", "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABAQDA== test@host", "test@host"},
		{"no_comment", "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABAQDA==", ""},
		{"only_two_fields", "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABAQDA==", ""},
		{
			"multi_word",
			"ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABAQDA== user@host additional info",
			"user@host additional info",
		},
		{"empty", "", ""},
		{"whitespace_trimmed", "  ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABAQDA== test@host  ", "test@host"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseComment(tt.content)
			assert.Equal(t, tt.want, got)
		})
	}
}

// assertCode checks DomainError code.
func assertCode(t *testing.T, err error, code errs.Code) {
	t.Helper()
	var de *errs.DomainError
	if errors.As(err, &de) {
		assert.Equal(t, code, de.Code)
	} else {
		t.Errorf("expected *errs.DomainError, got %T", err)
	}
}
