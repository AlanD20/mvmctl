package jailer

import "context"

// ProtectedCacheClient exposes only fixed protected-parent retirement.
type ProtectedCacheClient struct{ transport *TrustedReleaseClient }

// NewProtectedCacheClient binds the authenticated privileged receiver.
func NewProtectedCacheClient() *ProtectedCacheClient {
	return &ProtectedCacheClient{transport: NewTrustedReleaseClient()}
}

// Clean requests empty-parent cleanup, not recursive cache deletion.
func (client *ProtectedCacheClient) Clean(
	ctx context.Context, request ProtectedCacheCleanRequest,
) (ProtectedCacheCleanResult, error) {
	if err := request.Validate(); err != nil {
		return ProtectedCacheCleanResult{}, err
	}
	return exchangeTrustedRelease(ctx, client.transport, ProtectedCacheCleanAction, request, nil, 0,
		func(result ProtectedCacheCleanResult) error { return result.Validate() })
}
