package vmexecsessionwire

import (
	"bytes"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEncodeFrameAcceptsExactMaximum(t *testing.T) {
	body := bytes.Repeat([]byte("x"), maxFrameBytes)
	var encoded bytes.Buffer

	require.NoError(t, encodeFrame(t.Context(), &encoded, body))

	assert.Len(t, encoded.Bytes(), framePrefixBytes+maxFrameBytes)
	if diff := cmp.Diff(body, encoded.Bytes()[framePrefixBytes:]); diff != "" {
		t.Errorf("encoded maximum frame body mismatch (-want +got):\n%s", diff)
	}
}
