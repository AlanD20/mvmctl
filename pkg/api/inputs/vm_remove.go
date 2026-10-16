package inputs

import (
	"context"
	"regexp"

	"mvmctl/internal/core/vm"
)

var exactVMRemovalID = regexp.MustCompile(`^[0-9a-f]{32}$`)

// ResolveUnrecordedRemoval admits only a forced exact ID without a caller VM row.
// An empty ID leaves ordinary selector resolution and registered removal unchanged.
func (i *VMInput) ResolveUnrecordedRemoval(ctx context.Context, repo vm.Repository) (string, error) {
	if !i.Force || len(i.Identifiers) != 1 || !exactVMRemovalID.MatchString(i.Identifiers[0]) {
		return "", nil
	}
	id := i.Identifiers[0]
	if err := ctx.Err(); err != nil {
		return id, err
	}
	// Names take precedence over IDs in the ordinary VM resolver.
	named, err := repo.GetByName(ctx, id)
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
	if err := ctx.Err(); err != nil {
		return id, err
	}
	return id, nil
}
