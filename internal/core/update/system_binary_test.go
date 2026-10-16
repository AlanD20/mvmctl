package update

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/infra"
	"mvmctl/pkg/errs"
)

// Rationale: A group-authorized user must never replace the root-owned trust
// anchor through the ordinary self-update flow. A nil remote proves rejection
// happens before release lookup or any download.
func TestApply_RejectsSystemInstallationBeforeRemoteAccess(t *testing.T) {
	service := &Service{
		executable: func() (string, error) { return infra.SystemBinaryPath, nil },
		gh:         nil,
	}

	err := service.Apply(context.Background(), false)
	require.Error(t, err)
	assert.Equal(t, errs.CodeValidationFailed, errs.AsDomainError(err).Code)
	assert.ErrorContains(t, err, "sudo <new-mvm-binary> host install-system")
}

// Rationale: Lexical aliases of the canonical path identify the same target;
// an exact string comparison would let self-update replace the trust anchor.
func TestApply_RejectsCleanedSystemInstallationAliasBeforeRemoteAccess(t *testing.T) {
	service := &Service{
		executable: func() (string, error) { return "/usr/local/bin/../bin/mvm", nil },
		gh:         nil,
	}

	err := service.Apply(context.Background(), false)
	require.Error(t, err)
	assert.Equal(t, errs.CodeValidationFailed, errs.AsDomainError(err).Code)
	assert.ErrorContains(t, err, "sudo <new-mvm-binary> host install-system")
}
