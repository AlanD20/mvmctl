package jailer

import "context"

// RuntimeStatusCaller is supplied by the privileged dispatcher's authenticated peer, never the request body.
type RuntimeStatusCaller struct {
	UID uint32
	GID uint32
}

// RuntimeStatusServer exposes only batch observation of the existing schema-3 authority.
type RuntimeStatusServer struct {
	authority *instanceAuthority
}

// NewRuntimeStatusServer binds the existing descriptor-relative observer without opening authority yet.
func NewRuntimeStatusServer() *RuntimeStatusServer {
	return &RuntimeStatusServer{authority: newInstanceAuthority()}
}

// Observe returns no partial result and never performs lifecycle recovery or guest liveness probes.
// Public callers must remain inactive until every supported launch writes schema-3 authority.
func (server *RuntimeStatusServer) Observe(
	ctx context.Context,
	caller RuntimeStatusCaller,
	request RuntimeStatusRequest,
) (RuntimeStatusResult, error) {
	if err := request.Validate(); err != nil {
		return RuntimeStatusResult{}, err
	}
	if server == nil || server.authority == nil {
		return RuntimeStatusResult{}, runtimeStatusUnavailableError("", "runtime status server is not active", nil)
	}
	observed, err := server.authority.observeRuntimeStatus(
		ctx,
		instanceCaller{uid: caller.UID, gid: caller.GID},
		request.VMIDs,
	)
	if err != nil {
		return RuntimeStatusResult{}, err
	}
	result := RuntimeStatusResult{VMs: make([]RuntimeStatusObservation, 0, len(request.VMIDs))}
	for _, id := range request.VMIDs {
		result.VMs = append(result.VMs, RuntimeStatusObservation{VMID: id, Status: RuntimeStatus(observed[id])})
	}
	if err := result.Validate(request); err != nil {
		return RuntimeStatusResult{}, err
	}
	return result, nil
}
