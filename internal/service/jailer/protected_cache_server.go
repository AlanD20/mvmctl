package jailer

import (
	"context"
	"errors"
	"log/slog"

	"mvmctl/pkg/errs"
)

// ProtectedCacheServer owns explicit selected-store retirement.
type ProtectedCacheServer struct{}

// NewProtectedCacheServer constructs the fixed receiver without effects.
func NewProtectedCacheServer() *ProtectedCacheServer { return &ProtectedCacheServer{} }

// Clean authenticates ownership and retires only the complete admitted empty-parent inventory.
func (server *ProtectedCacheServer) Clean(
	ctx context.Context, uid, gid uint32, request ProtectedCacheCleanRequest,
) (_ ProtectedCacheCleanResult, returnErr error) {
	defer func() {
		if returnErr != nil {
			slog.Error("protected cache cleanup refused", "error", returnErr)
			returnErr = errs.WrapMsg(errs.CodeCacheCleanFailed, "retire protected cache parents", returnErr)
		}
	}()
	if err := request.Validate(); err != nil {
		return ProtectedCacheCleanResult{}, err
	}
	cache, err := pinSnapshotCache(ctx, instanceCaller{uid: uid, gid: gid}, request.CacheRoot)
	if err != nil {
		return ProtectedCacheCleanResult{}, err
	}
	defer func() { returnErr = errors.Join(returnErr, cache.Release(context.WithoutCancel(ctx))) }()
	if err := cleanProtectedCache(ctx, cache); err != nil {
		return ProtectedCacheCleanResult{}, err
	}
	return ProtectedCacheCleanResult{Cleaned: true}, nil
}
