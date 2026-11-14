package host

import "context"

type sudoersConfigDeps struct {
	verifySystemBinary func() error
	store              sudoersStoreDeps
	policy             sudoersStorePolicy
}

// ConfigureSudoers reconciles the single project-owned sudoers drop-in.
// Its fixed group, path, and content are not caller-selectable.
func (s *Service) ConfigureSudoers(ctx context.Context) (bool, error) {
	return configureSudoers(ctx, sudoersConfigDeps{
		verifySystemBinary: verifyCanonicalSystemBinaryForSudoers,
		store:              realSudoersStoreDeps(),
		policy:             sudoersStorePolicy{rootPath: "/", uid: 0, gid: 0},
	})
}

func configureSudoers(ctx context.Context, deps sudoersConfigDeps) (bool, error) {
	if err := deps.verifySystemBinary(); err != nil {
		return false, err
	}
	return publishSudoers(ctx, deps.store, deps.policy)
}
