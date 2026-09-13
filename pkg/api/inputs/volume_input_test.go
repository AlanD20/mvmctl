package inputs_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/lib/model"
	"mvmctl/internal/testutil"
	"mvmctl/pkg/api/inputs"
	"mvmctl/pkg/errs"
)

func TestVolumeInputResolveRaw(t *testing.T) {
	for name, tc := range map[string]struct {
		identifiers []string
		code        errs.Code
	}{
		"qcow2":            {[]string{"qcow"}, errs.CodeValidationFailed},
		"unknown":          {[]string{"unknown"}, errs.CodeValidationFailed},
		"empty format":     {[]string{"empty"}, errs.CodeValidationFailed},
		"missing":          {[]string{"missing"}, errs.CodeVolumeNotFound},
		"partial":          {[]string{"raw", "missing"}, errs.CodeVolumeNotFound},
		"mixed formats":    {[]string{"raw", "qcow"}, errs.CodeValidationFailed},
		"raw deduplicated": {[]string{"raw", "raw-id"}, ""},
	} {
		t.Run(name, func(t *testing.T) {
			repo := testutil.NewVolumeRepo()
			raw := &model.VolumeItem{
				ID: "raw-id", Name: "raw", Format: model.VolumeFormatRaw,
				IsReadOnly: true, IsShareable: true, CacheType: model.CacheTypeWriteback,
			}
			for _, vol := range []*model.VolumeItem{
				raw,
				{ID: "qcow-id", Name: "qcow", Format: model.VolumeFormatQCOW2},
				{ID: "unknown-id", Name: "unknown", Format: "unknown"},
				{ID: "empty-id", Name: "empty"},
			} {
				require.NoError(t, repo.Upsert(t.Context(), vol))
			}
			input := inputs.VolumeInput{Identifiers: tc.identifiers}
			wantRaw := *raw
			got, err := input.ResolveRaw(t.Context(), repo)
			if tc.code != "" {
				require.Error(t, err)
				assert.Equal(t, tc.code, errs.AsDomainError(err).Code)
				assert.Nil(t, got, "failed resolution must not return usable partial volumes")
				return
			}
			require.NoError(t, err)
			testutil.AssertDiff(t, []*model.VolumeItem{&wantRaw}, got)
		})
	}
}
