package vmexecsessionwire

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEncodeFrameAcceptsExactMaximum(t *testing.T) {
	body := bytes.Repeat([]byte("x"), maxFrameBytes)
	var encoded bytes.Buffer

	require.NoError(t, encodeFrame(t.Context(), &encoded, body))

	assert.Len(t, encoded.Bytes(), framePrefixBytes+maxFrameBytes)
	assert.Equal(t, body, encoded.Bytes()[framePrefixBytes:])
}
