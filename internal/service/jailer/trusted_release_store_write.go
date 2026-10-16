package jailer

import "context"

type trustedReleaseStoreWriteLease struct {
	store *trustedReleaseStore
}

// CRITICAL: This capability creates only the fixed managed store base. Architecture/version slots and leaves remain
// separate typed operations so a caller cannot turn base preparation into a generic root filesystem write primitive.
func openTrustedReleaseStoreForWrite(
	ctx context.Context,
	deps trustedReleaseStoreDeps,
	policy trustedReleaseStorePolicy,
) (*trustedReleaseStoreWriteLease, error) {
	store, err := openTrustedReleaseStore(ctx, deps, policy, true)
	if err != nil {
		return nil, err
	}
	return &trustedReleaseStoreWriteLease{store: store}, nil
}

func (lease *trustedReleaseStoreWriteLease) Release(ctx context.Context) error {
	if lease == nil || lease.store == nil {
		return nil
	}
	err := lease.store.Release(ctx)
	lease.store = nil
	return err
}
