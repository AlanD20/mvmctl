package api

import (
	"crypto/sha256"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/testutil"
	"mvmctl/pkg/api/inputs"
	"mvmctl/pkg/errs"
)

func TestImageRemoveWithoutExportPreservesLocalAbsence(t *testing.T) {
	// A supported full-ID orphan selector must not enroll the host just to
	// return ordinary not-found. No privileged service or installed candidate
	// is supplied; the per-test ID has no read-only root export presence hint.
	id := fmt.Sprintf("%x", sha256.Sum256([]byte(t.TempDir())))
	present, err := hasVMImageExportData(t.Context(), id)
	require.NoError(t, err)
	require.False(t, present)
	require.NoError(t, releasePresentVMImageExport(t.Context(), id))
	op := &Operation{Repos: Repos{Image: testutil.NewImageRepo()}}
	result := op.ImageRemove(t.Context(), inputs.ImageInput{Identifiers: []string{id}}, true)
	require.Len(t, result.Items, 1)
	assert.Equal(t, "error", result.Items[0].Status)
	assert.Equal(t, string(errs.CodeImageNotFound), result.Items[0].Code)
}
