package inputs

import (
	"context"
	"regexp"

	"mvmctl/internal/core/image"
)

var exactImageExportRemovalID = regexp.MustCompile(`^[0-9a-f]{64}$`)

// ResolveUnrecordedExportRemoval admits only forced exact absence, preserving
// type/name precedence and soft-deleted rows. Resolver error strings are not proof.
func (input *ImageInput) ResolveUnrecordedExportRemoval(
	ctx context.Context,
	repo image.Repository,
	force bool,
) (string, error) {
	if !force || len(input.Identifiers) != 1 || !exactImageExportRemovalID.MatchString(input.Identifiers[0]) {
		return "", nil
	}
	id := input.Identifiers[0]
	if err := ctx.Err(); err != nil {
		return id, err
	}
	typed, err := repo.GetByType(ctx, id)
	if err != nil {
		return id, err
	}
	if typed != nil {
		return "", nil
	}
	named, err := repo.GetByName(ctx, id, true)
	if err != nil {
		return id, err
	}
	if named != nil {
		return "", nil
	}
	registered, err := repo.Get(ctx, id)
	if err != nil {
		return id, err
	}
	if registered != nil {
		return "", nil
	}
	matches, err := repo.FindByPrefix(ctx, id, true)
	if err != nil {
		return id, err
	}
	if len(matches) != 0 {
		return "", nil
	}
	if err := ctx.Err(); err != nil {
		return id, err
	}
	return id, nil
}
