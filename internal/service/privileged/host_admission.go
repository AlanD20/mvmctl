package privileged

import (
	"errors"
	"maps"

	"mvmctl/pkg/errs"
)

func appendHostAdmissionReleaseError(primary, cleanup error) error {
	if cleanup == nil {
		return primary
	}
	if primary == nil {
		primary = cleanup
	}
	domain := errs.AsDomainError(primary)
	if domain == nil {
		return errs.WrapMsg(errs.CodeInternal, "release host admission", errors.Join(primary, cleanup),
			errs.WithDetails(map[string]any{"host_admission_release_failed": true}))
	}
	result := *domain
	result.Details = maps.Clone(domain.Details)
	if result.Details == nil {
		result.Details = make(map[string]any)
	}
	result.Details["host_admission_release_failed"] = true
	result.Err = errors.Join(primary, cleanup)
	return &result
}
