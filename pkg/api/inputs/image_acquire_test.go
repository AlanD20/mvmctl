package inputs_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/core/config"
	"mvmctl/internal/lib/model"
	"mvmctl/internal/testutil"
	"mvmctl/pkg/api/inputs"
	"mvmctl/pkg/errs"
)

func TestImageImportResumeRejectsAmbiguousIdentity(t *testing.T) {
	for name, input := range map[string]inputs.ImageImportInput{
		"prefix":    {ResumeID: "abcd"},
		"path":      {ResumeID: strings.Repeat("a", 63) + "/"},
		"uppercase": {ResumeID: strings.Repeat("A", 64)},
		"replace":   {ResumeID: strings.Repeat("a", 64), Force: true},
	} {
		t.Run(name, func(t *testing.T) {
			input.Name, input.Source = "resume", "source.img"
			require.Error(t, input.Validate())
		})
	}
	input := inputs.ImageImportInput{Name: "resume", Source: "source.img", ResumeID: strings.Repeat("a", 64)}
	require.NoError(t, input.Validate())
}

func TestImageImportResumeRefusesRegisteredAndVMSourceBeforeFileEffects(t *testing.T) {
	for _, registered := range []bool{false, true} {
		t.Run(map[bool]string{true: "registered image", false: "VM source"}[registered], func(t *testing.T) {
			images := testutil.NewImageRepo()
			vms := testutil.NewVMRepo()
			id := strings.Repeat("a", 64)
			if registered {
				require.NoError(t, images.Upsert(t.Context(), &model.ImageItem{ID: id}))
			} else {
				require.NoError(t, vms.Upsert(t.Context(), &model.VMItem{ID: "vm", Name: "source"}))
			}
			input := inputs.ImageImportInput{Name: "resume", Source: "source", ResumeID: id}
			_, err := input.Resolve(t.Context(), nil, images, vms)
			require.Error(t, err)
			var domainErr *errs.DomainError
			require.ErrorAs(t, err, &domainErr)
			require.Equal(t, errs.CodeValidationFailed, domainErr.Code)
		})
	}
}

func TestImageImportInputResolveFormat(t *testing.T) {
	// Rationale: auto is a public selection policy, not an extraction format.
	// Both its default and explicit forms must resolve to a concrete format
	// without replacing concrete config defaults or explicit overrides.
	tests := map[string]struct {
		sourceName   string
		sourceBytes  []byte
		inputFormat  string
		configFormat string
		wantFormat   string
		wantDetected string
	}{
		"omitted_format_uses_default_auto_raw_extension": {
			sourceName: "rootfs.raw", sourceBytes: make([]byte, 1024), wantFormat: "raw",
		},
		"explicit_auto_uses_raw_extension_over_config": {
			sourceName: "rootfs.raw", sourceBytes: make([]byte, 1024),
			inputFormat: "auto", configFormat: "qcow2", wantFormat: "raw",
		},
		"explicit_auto_uses_unlabelled_qcow2_magic": {
			sourceName: "unlabelled", sourceBytes: []byte{'Q', 'F', 'I', 0xfb},
			inputFormat: "auto", configFormat: "raw", wantFormat: "qcow2", wantDetected: "qcow2",
		},
		"omitted_format_preserves_concrete_config_default": {
			sourceName: "rootfs.raw", sourceBytes: make([]byte, 1024),
			configFormat: "qcow2", wantFormat: "qcow2",
		},
		"explicit_format_overrides_config_and_detected_format": {
			sourceName: "unlabelled", sourceBytes: []byte{'Q', 'F', 'I', 0xfb},
			inputFormat: "raw", configFormat: "qcow2", wantFormat: "raw", wantDetected: "qcow2",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			source := filepath.Join(t.TempDir(), tc.sourceName)
			require.NoError(t, os.WriteFile(source, tc.sourceBytes, 0600))
			configRepo := testutil.NewConfigRepo()
			if tc.configFormat != "" {
				require.NoError(t, configRepo.Set(t.Context(), "defaults.image", "import_format", tc.configFormat))
			}
			input := inputs.ImageImportInput{Name: "format-regression", Source: source, Format: tc.inputFormat}
			resolved, err := input.Resolve(
				t.Context(),
				config.NewService(configRepo, config.NewConstraintRegistry()),
				testutil.NewImageRepo(),
				testutil.NewVMRepo(),
			)
			require.NoError(t, err)
			require.NotNil(t, resolved)
			want := struct{ Format, Detected string }{tc.wantFormat, tc.wantDetected}
			got := struct{ Format, Detected string }{resolved.Format, resolved.FormatDetected}
			if diff := cmp.Diff(want, got); diff != "" {
				t.Errorf("resolved import format mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
