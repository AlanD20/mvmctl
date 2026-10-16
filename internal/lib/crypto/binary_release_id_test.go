package crypto_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"mvmctl/internal/lib/crypto"
)

func TestBinaryReleaseIDHasStableIndependentMembers(t *testing.T) {
	tests := map[string]struct {
		binaryType string
		digest     string
		want       string
	}{
		"firecracker": {
			binaryType: "firecracker",
			digest:     "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			want:       "f01f4d915d62e479e1d235e67eef481b7302309fcdde08e09a7864c5889c9c2a",
		},
		"jailer": {
			binaryType: "jailer",
			digest:     "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
			want:       "9ad60ba0c2da2e4945938e0d780c2adf66f3200c3526046934a3b0b293123fda",
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got := crypto.BinaryReleaseID(tc.binaryType, "x86_64", "1.16.1", tc.digest)
			assert.Equal(t, tc.want, got)
		})
	}
}
