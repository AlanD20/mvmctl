package inputs_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/core/image"
	"mvmctl/internal/lib/model"
	"mvmctl/internal/testutil"
	"mvmctl/pkg/api/inputs"
)

func TestImageExportRemovalRequiresForcedExactAbsence(t *testing.T) {
	// Orphan cleanup must not steal a ordinary selector or turn a database
	// failure into evidence that privileged, root-only data can be canceled.
	id := strings.Repeat("a", 64)
	deleted := "2026-10-07"
	for name, test := range map[string]struct {
		selectors []string
		force     bool
		row       *model.ImageItem
		want      string
	}{
		"exact_absent":  {selectors: []string{id}, force: true, want: id},
		"not_forced":    {selectors: []string{id}},
		"prefix":        {selectors: []string{id[:12]}, force: true},
		"uppercase":     {selectors: []string{strings.ToUpper(id)}, force: true},
		"multiple":      {selectors: []string{id, strings.Repeat("b", 64)}, force: true},
		"ordinary_type": {selectors: []string{id}, force: true, row: &model.ImageItem{ID: "typed", Type: id, IsPresent: true}},
		"ordinary_name": {selectors: []string{id}, force: true, row: &model.ImageItem{ID: "named", Name: id, IsPresent: true}},
		"deleted_name":  {selectors: []string{id}, force: true, row: &model.ImageItem{ID: "deleted", Name: id, DeletedAt: &deleted}},
		"deleted_exact": {selectors: []string{id}, force: true, row: &model.ImageItem{ID: id, DeletedAt: &deleted}},
	} {
		t.Run(name, func(t *testing.T) {
			repo := testutil.NewImageRepo()
			if test.row != nil {
				require.NoError(t, repo.Upsert(t.Context(), test.row))
			}
			input := inputs.ImageInput{Identifiers: test.selectors}
			got, err := input.ResolveUnrecordedExportRemoval(t.Context(), repo, test.force)
			require.NoError(t, err)
			assert.Equal(t, test.want, got)
		})
	}
}

type imageExportRemovalUnavailableRepo struct {
	image.Repository
	failure error
}

func (repo imageExportRemovalUnavailableRepo) GetByType(context.Context, string) (*model.ImageItem, error) {
	return nil, repo.failure
}

func TestImageExportRemovalNeverConvertsLookupFailureToAbsence(t *testing.T) {
	id := strings.Repeat("a", 64)
	input := inputs.ImageInput{Identifiers: []string{id}}
	failure := errors.New("repository unavailable")
	got, err := input.ResolveUnrecordedExportRemoval(t.Context(), imageExportRemovalUnavailableRepo{
		Repository: testutil.NewImageRepo(), failure: failure,
	}, true)
	assert.Equal(t, id, got)
	assert.ErrorIs(t, err, failure)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	got, err = input.ResolveUnrecordedExportRemoval(ctx, testutil.NewImageRepo(), true)
	assert.Equal(t, id, got)
	assert.ErrorIs(t, err, context.Canceled)
}
