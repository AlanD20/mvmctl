package inputs_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/lib/model"
	"mvmctl/internal/testutil"
	"mvmctl/pkg/api/inputs"
	"mvmctl/pkg/errs"
)

func TestImageInputResolveLongSelectors(t *testing.T) {
	// Rationale: Imported-kernel runner image names embed a full kernel ID;
	// lookup must accept the names and version selectors that import creates.
	runnerType := "mvm-test-runner-kernel-d6175bf7d6854899b066d73f52adc1e3c07fe6382a2a2585a6e60f6c77d0396f"
	runnerVersion := "0.3.0-rc.integrated-64079c3b"
	runner := &model.ImageItem{
		ID: strings.Repeat("a", 64), Type: runnerType, Version: runnerVersion,
		Name: runnerType + " " + runnerVersion, IsPresent: true, IsImported: true,
	}
	otherVersion := &model.ImageItem{
		ID: strings.Repeat("b", 64), Type: runnerType, Version: "0.3.0-other",
		Name: "other runner", IsPresent: true, IsDefault: true,
	}
	for name, tc := range map[string]struct {
		identifiers []string
		want        *model.ImageItem
		code        errs.Code
		wantError   string
	}{
		"empty identifiers": {wantError: "at least one image identifier is required"},
		"missing long identifier": {
			identifiers: []string{runnerType + "-missing"}, code: errs.CodeImageNotFound,
		},
		"full kernel selector selects exact version": {
			identifiers: []string{runnerType + ":" + runnerVersion}, want: runner,
		},
		"bare long type":    {identifiers: []string{runnerType}, want: otherVersion},
		"long display name": {identifiers: []string{runner.Name}, want: runner},
		"full image ID":     {identifiers: []string{runner.ID}, want: runner},
	} {
		t.Run(name, func(t *testing.T) {
			repo := testutil.NewImageRepo()
			require.NoError(t, repo.Upsert(t.Context(), runner))
			require.NoError(t, repo.Upsert(t.Context(), otherVersion))
			input := inputs.ImageInput{Identifiers: tc.identifiers}
			got, err := input.Resolve(t.Context(), repo)
			if tc.code != "" {
				var domainErr *errs.DomainError
				require.ErrorAs(t, err, &domainErr)
				assert.Equal(t, tc.code, domainErr.Code)
				return
			}
			if tc.wantError != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantError)
				return
			}
			require.NoError(t, err)
			testutil.AssertDiff(t, []*model.ImageItem{tc.want}, got)
		})
	}
}
