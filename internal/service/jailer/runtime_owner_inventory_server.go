package jailer

import (
	"context"
	"io"
	"log/slog"
	"sort"
	"strconv"
	"strings"

	"mvmctl/internal/lib/privilegedwire"
)

// RuntimeOwnerInventoryServer observes root-owned records without a user-database selection.
type RuntimeOwnerInventoryServer struct {
	authority *instanceAuthority
}

// NewRuntimeOwnerInventoryServer binds the existing runtime authority without opening it.
func NewRuntimeOwnerInventoryServer() *RuntimeOwnerInventoryServer {
	return &RuntimeOwnerInventoryServer{authority: newInstanceAuthority()}
}

// Observe retains the index across enumeration and observation so newly published records cannot be omitted.
// Every process observation uses the existing pidfd-bound observer; no recovery or signaling occurs here.
func (server *RuntimeOwnerInventoryServer) Observe(
	ctx context.Context,
	caller RuntimeStatusCaller,
	_ RuntimeOwnerInventoryRequest,
) (result RuntimeOwnerInventoryResult, returnErr error) {
	defer func() {
		if returnErr != nil {
			result = RuntimeOwnerInventoryResult{}
			slog.Error("runtime owner inventory failed", "caller_uid", caller.UID, "error", returnErr)
		}
	}()
	if ctx == nil {
		return result, instanceValidationError("runtime owner inventory context is required")
	}
	if err := ctx.Err(); err != nil {
		return result, runtimeStatusUnavailableError("", "runtime owner inventory context ended", err)
	}
	privateCaller := instanceCaller{uid: caller.UID, gid: caller.GID}
	if err := validateInstanceCaller(privateCaller); err != nil {
		return result, err
	}
	if server == nil || server.authority == nil || server.authority.authorityTree == nil {
		return result, runtimeStatusUnavailableError("", "runtime owner inventory server is not active", nil)
	}
	authority := server.authority
	identity := realProcessIdentityVerifierDeps()
	recovery := realProcessRecoveryDeps()
	bootID, err := identity.readBootID(ctx)
	if err != nil || !hostBootIDPattern.MatchString(bootID) {
		return result, runtimeStatusUnavailableError("", "read runtime owner inventory boot identity", err)
	}
	defer func() {
		if returnErr != nil {
			return
		}
		after, err := identity.readBootID(ctx)
		if err != nil || after != bootID {
			returnErr = runtimeStatusUnavailableError("", "runtime owner inventory boot identity changed", err)
		}
	}()
	tree, err := authority.authorityTree.openJailer(ctx)
	if err != nil {
		return result, err
	}
	defer func() {
		returnErr = appendInstanceOperationError(returnErr, "release runtime inventory authority",
			tree.Release(context.WithoutCancel(ctx)))
	}()
	index, err := tree.acquireIndex(ctx)
	if err != nil {
		return result, err
	}
	defer func() {
		returnErr = appendInstanceOperationError(returnErr, "release runtime inventory index",
			index.Release(context.WithoutCancel(ctx)))
	}()
	roots, found, err := openRuntimeStatusState(ctx, authority.deps, authority.policy, tree)
	if err != nil {
		return result, err
	}
	defer func() {
		returnErr = appendInstanceOperationError(returnErr, "close runtime inventory state",
			closeInstanceFDs(context.WithoutCancel(ctx), roots.deps, roots.retained))
	}()
	result.VMs = make([]RuntimeStatusObservation, 0)
	if !found {
		return result, nil
	}
	ownerNames, err := roots.deps.readDirNames(ctx, roots.stateFD)
	if err != nil {
		return result, instanceAtomicError("enumerate runtime inventory owners", err)
	}
	ownerFound := false
	for _, name := range ownerNames {
		uid, valid := validateCanonicalUIDName(name)
		if !valid {
			return result, instanceAtomicError("runtime inventory owner directory name is invalid", nil)
		}
		ownerFound = ownerFound || uid == caller.UID
	}
	if !ownerFound {
		return result, nil
	}
	fd, err := openInstanceDirectory(ctx, roots.deps, roots.stateFD, strconv.FormatUint(uint64(caller.UID), 10),
		false, instancePrivateDirectory, roots.policy)
	if err != nil {
		return result, err
	}
	directories, err := roots.openAuthorityForLocation(ctx, &instanceLocation{uid: caller.UID, stateFD: fd})
	if err != nil {
		return result, err
	}
	defer func() {
		returnErr = appendInstanceOperationError(returnErr, "release runtime inventory owner",
			directories.Release(context.WithoutCancel(ctx)))
	}()
	names, err := roots.deps.readDirNames(ctx, directories.stateFD)
	if err != nil {
		return result, instanceAtomicError("enumerate runtime owner inventory records", err)
	}
	sort.Strings(names)
	ids := make([]string, 0, len(names))
	for _, name := range names {
		if isInstanceTempName(name) {
			continue
		}
		id, valid := strings.CutSuffix(name, ".json")
		if !valid || !vmIDPattern.MatchString(id) {
			return result, instanceAtomicError("unexpected runtime owner inventory entry", nil)
		}
		ids = append(ids, id)
	}
	owners, err := roots.findRuntimeStatusOwners(ctx, ids)
	if err != nil {
		return result, err
	}
	for _, id := range ids {
		if owner, exists := owners[id]; !exists || owner != caller.UID {
			return result, instanceAtomicError("runtime inventory ownership changed while locked", nil)
		}
		if err := ctx.Err(); err != nil {
			return result, runtimeStatusUnavailableError(id, "runtime owner inventory context ended", err)
		}
		status, err := observeLockedRuntimeStatus(ctx, privateCaller, directories, id, bootID, recovery, identity)
		if err != nil {
			return result, err
		}
		result.VMs = append(result.VMs, RuntimeStatusObservation{VMID: id, Status: RuntimeStatus(status)})
	}
	if err := result.Validate(); err != nil {
		return result, err
	}
	// Apply the existing transport's exact response bounds without truncating a caller's inventory.
	if err := privilegedwire.EncodeSuccess(ctx, io.Discard, RuntimeOwnerInventoryAction, result); err != nil {
		return result, runtimeStatusUnavailableError("", "runtime owner inventory exceeds response bounds", err)
	}
	return result, nil
}
