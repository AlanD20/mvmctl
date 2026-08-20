package main

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"mvmctl/internal/service/privileged"
)

func TestRun_RoutesPrivilegedInvocationBeforeNormalInitialization(t *testing.T) {
	sentinel := errors.New("privileged sentinel")
	privilegedCalls := 0
	normalCalls := 0

	exitCode := run(context.Background(), []string{privileged.Marker, "unknown"}, strings.NewReader("{}"), entrypoints{
		privileged: func(context.Context, []string, io.Reader) error {
			privilegedCalls++
			return sentinel
		},
		normal: func() int {
			normalCalls++
			return 0
		},
	})

	assert.Equal(t, 1, exitCode)
	assert.Equal(t, 1, privilegedCalls)
	assert.Zero(t, normalCalls)
}

// Rationale: Unsupported protocol versions must remain inside the reserved
// boundary instead of falling through to Cobra or user-state initialization.
func TestRun_RoutesUnsupportedPrivilegedVersionToFailClosedEntry(t *testing.T) {
	privilegedCalls := 0
	normalCalls := 0

	exitCode := run(context.Background(), []string{"__mvm_privileged_v999", "unknown"}, nil, entrypoints{
		privileged: func(context.Context, []string, io.Reader) error {
			privilegedCalls++
			return nil
		},
		normal: func() int {
			normalCalls++
			return 0
		},
	})

	assert.Zero(t, exitCode)
	assert.Equal(t, 1, privilegedCalls)
	assert.Zero(t, normalCalls)
}

func TestRun_RoutesOrdinaryArgumentsToNormalCLI(t *testing.T) {
	privilegedCalls := 0
	normalCalls := 0

	exitCode := run(context.Background(), []string{"vm", "list"}, nil, entrypoints{
		privileged: func(context.Context, []string, io.Reader) error {
			privilegedCalls++
			return nil
		},
		normal: func() int {
			normalCalls++
			return 7
		},
	})

	assert.Equal(t, 7, exitCode)
	assert.Zero(t, privilegedCalls)
	assert.Equal(t, 1, normalCalls)
}
