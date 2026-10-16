package host

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"mvmctl/pkg/errs"
)

// Rationale: Reset reports must survive multiple deferred releases without losing
// the original guard failure, retry classification, or redaction of runner output.
func TestResetFailurePreservesPrimaryThroughCleanup(t *testing.T) {
	process := errs.WrapMsg(errs.CodeProcessError,
		"Command failed (exit 7): /private-command private-argument\nprivate-output", unix.EPERM)
	metadata := map[string]any{"authority_retained": true}
	primary := errs.WrapMsg(errs.CodeVMAtomicFailed, "inspect root authority",
		fmt.Errorf("guard wrapper: %w", errors.Join(unix.EROFS, process)),
		errs.WithClass(errs.ClassRetryable), errs.WithDetails(metadata))
	guardCleanup := errors.New("guard cleanup refused")
	first := resetFailure("acquire Jailer reset guard", errors.Join(primary, guardCleanup), nil)
	cleanup := errs.WrapMsg(errs.CodeNetworkError, "release failure", unix.EIO,
		errs.WithDetails(map[string]any{
			"cleanup_retained": true, "reset_failure_stage": "cleanup", "reset_failure_cause": "replacement",
		}))
	result := resetFailure("release reset files", first, cleanup)
	result = resetFailure("release administrator admission", result, unix.EBADF)

	for _, cause := range []error{primary, guardCleanup, process, unix.EROFS, unix.EPERM, cleanup, unix.EIO, unix.EBADF} {
		assert.ErrorIs(t, result, cause)
	}
	var domain *errs.DomainError
	require.ErrorAs(t, result, &domain)
	assert.Equal(t, errs.CodeHostResetFailed, domain.Code)
	assert.Equal(t, errs.ClassRetryable, domain.Class)
	assert.Equal(t, "host", domain.Op)
	assert.Equal(t, "", domain.Entity)
	assert.Equal(t, "acquire Jailer reset guard", domain.Details["reset_failure_stage"])
	diagnostic, ok := domain.Details["reset_failure_cause"].(string)
	require.True(t, ok)
	assert.Contains(t, diagnostic, "inspect root authority")
	assert.Contains(t, diagnostic, unix.EROFS.Error())
	assert.Contains(t, diagnostic, string(errs.CodeProcessError))
	assert.Contains(t, diagnostic, unix.EPERM.Error())
	assert.Contains(t, diagnostic, "guard cleanup refused")
	assert.NotContains(t, diagnostic, "replacement")
	for _, private := range []string{"/private-command", "private-argument", "private-output"} {
		assert.NotContains(t, domain.Message, private)
		assert.NotContains(t, diagnostic, private)
	}
	assert.Contains(t, domain.Message, "acquire Jailer reset guard")
	assert.Contains(t, domain.Message, diagnostic)
	assert.Equal(t, errs.AsDomainError(first).Message, domain.Message)
	assert.Equal(t, true, domain.Details["journal_retained"])
	assert.Equal(t, true, domain.Details["authority_retained"])
	assert.Equal(t, true, domain.Details["cleanup_retained"])
	if diff := cmp.Diff(map[string]any{"authority_retained": true}, metadata); diff != "" {
		t.Errorf("source error metadata changed (-want +got):\n%s", diff)
	}
}

// Rationale: A guard with a long Unicode/control-bearing cause must remain a
// failure after cleanup while its public diagnostic stays bounded and printable.
func TestResetFailureBoundsPublicCauseAfterCleanup(t *testing.T) {
	primary := errors.New("\x1b\n" + strings.Repeat("界", 300))
	first := resetFailure("preflight operator group", primary, nil)
	result := resetFailure("close administrator state", first, unix.EIO)
	assert.ErrorIs(t, result, primary)
	assert.ErrorIs(t, result, unix.EIO)
	domain := errs.AsDomainError(result)
	require.NotNil(t, domain)
	diagnostic, ok := domain.Details["reset_failure_cause"].(string)
	require.True(t, ok)
	assert.LessOrEqual(t, len(diagnostic), 512)
	assert.True(t, utf8.ValidString(diagnostic))
	assert.False(t, strings.ContainsAny(diagnostic, "\x1b\n\r\x00"))
	assert.True(t, strings.HasPrefix(diagnostic, "\\x1b\\n界"))
	assert.True(t, strings.HasSuffix(diagnostic, "..."))
	assert.Equal(t, "preflight operator group", domain.Details["reset_failure_stage"])
	assert.Equal(t, errs.AsDomainError(first).Message, domain.Message)
}
