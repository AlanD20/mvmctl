package jailer

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
)

type instanceLocation struct {
	uid     uint32
	stateFD int
}

func (roots *instanceAuthorityRoots) findInstance(
	ctx context.Context,
	vmID string,
) (_ *instanceLocation, returnErr error) {
	names, err := roots.deps.readDirNames(ctx, roots.stateFD)
	if err != nil {
		return nil, instanceAtomicError("enumerate instance authority owners", err)
	}
	sort.Strings(names)
	var found *instanceLocation
	defer func() {
		if returnErr == nil || found == nil {
			return
		}
		if err := roots.deps.close(context.WithoutCancel(ctx), found.stateFD); err != nil {
			returnErr = appendInstanceOperationError(
				returnErr,
				"close matched instance authority directory",
				err,
			)
		}
	}()
	for _, name := range names {
		uid, ok := validateCanonicalUIDName(name)
		if !ok {
			return nil, instanceAtomicError("instance authority owner directory name is invalid", nil)
		}
		stateFD, err := openInstanceDirectory(ctx, roots.deps, roots.stateFD, name, false, true, roots.policy)
		if err != nil {
			return nil, err
		}
		directories := &instanceUIDDirectories{
			deps:      roots.deps,
			policy:    roots.policy,
			uid:       uid,
			stateFD:   stateFD,
			runtimeFD: -1,
		}
		_, exists, readErr := directories.readRecord(ctx, vmID)
		if readErr != nil {
			return nil, appendInstanceOperationError(
				readErr,
				"close rejected instance authority owner directory",
				roots.deps.close(context.WithoutCancel(ctx), stateFD),
			)
		}
		if !exists {
			if closeErr := roots.deps.close(context.WithoutCancel(ctx), stateFD); closeErr != nil {
				return nil, instanceAtomicError("close scanned instance authority directory", closeErr)
			}
			continue
		}
		if found != nil {
			closeErr := roots.deps.close(context.WithoutCancel(ctx), stateFD)
			if closeErr != nil {
				return nil, instanceAtomicError("close duplicate instance authority directory", closeErr)
			}
			return nil, instanceAtomicError("duplicate global VM authority claim", nil)
		}
		found = &instanceLocation{uid: uid, stateFD: stateFD}
	}
	return found, nil
}

func (roots *instanceAuthorityRoots) openRuntimeForLocation(
	ctx context.Context,
	location *instanceLocation,
) (*instanceUIDDirectories, error) {
	name := fmt.Sprintf("%d", location.uid)
	runtimeFD, err := openInstanceDirectory(ctx, roots.deps, roots.runtimeFD, name, true, true, roots.policy)
	if err != nil {
		if closeErr := roots.deps.close(context.WithoutCancel(ctx), location.stateFD); closeErr != nil {
			err = appendInstanceOperationError(err, "close located instance authority directory", closeErr)
		}
		return nil, err
	}
	return &instanceUIDDirectories{
		deps:      roots.deps,
		policy:    roots.policy,
		uid:       location.uid,
		stateFD:   location.stateFD,
		runtimeFD: runtimeFD,
	}, nil
}

func (roots *instanceAuthorityRoots) releaseIsReferenced(
	ctx context.Context,
	release releaseIdentity,
) (bool, error) {
	ownerNames, err := roots.deps.readDirNames(ctx, roots.stateFD)
	if err != nil {
		return false, instanceAtomicError("enumerate release authority owners", err)
	}
	sort.Strings(ownerNames)
	seenVMs := make(map[string]struct{})
	for _, ownerName := range ownerNames {
		uid, ok := validateCanonicalUIDName(ownerName)
		if !ok {
			return false, instanceAtomicError("instance authority owner directory name is invalid", nil)
		}
		stateFD, err := openInstanceDirectory(
			ctx,
			roots.deps,
			roots.stateFD,
			ownerName,
			false,
			true,
			roots.policy,
		)
		if err != nil {
			return false, err
		}
		referenced, scanErr := roots.releaseReferencedByOwner(ctx, release, uid, stateFD, seenVMs)
		closeErr := roots.deps.close(context.WithoutCancel(ctx), stateFD)
		if scanErr != nil {
			return false, appendInstanceOperationError(
				scanErr,
				"close release-scan instance directory",
				closeErr,
			)
		}
		if closeErr != nil {
			return false, instanceAtomicError("close release-scan instance directory", closeErr)
		}
		if referenced {
			return true, nil
		}
	}
	return false, nil
}

func (roots *instanceAuthorityRoots) releaseReferencedByOwner(
	ctx context.Context,
	release releaseIdentity,
	uid uint32,
	stateFD int,
	seenVMs map[string]struct{},
) (bool, error) {
	names, err := roots.deps.readDirNames(ctx, stateFD)
	if err != nil {
		return false, instanceAtomicError("enumerate instance authority records", err)
	}
	sort.Strings(names)
	for _, name := range names {
		if isInstanceTempName(name) {
			continue
		}
		if !strings.HasSuffix(name, ".json") {
			return false, instanceAtomicError("unexpected instance authority entry", nil)
		}
		vmID := strings.TrimSuffix(name, ".json")
		if !vmIDPattern.MatchString(vmID) {
			return false, instanceAtomicError("instance authority record name is invalid", nil)
		}
		if _, exists := seenVMs[vmID]; exists {
			return false, instanceAtomicError("duplicate global VM authority claim", nil)
		}
		seenVMs[vmID] = struct{}{}
		runtimeFD, err := openInstanceDirectory(
			ctx,
			roots.deps,
			roots.runtimeFD,
			fmt.Sprintf("%d", uid),
			true,
			true,
			roots.policy,
		)
		if err != nil {
			return false, err
		}
		directories := &instanceUIDDirectories{
			deps: roots.deps, policy: roots.policy, uid: uid, stateFD: stateFD, runtimeFD: runtimeFD,
		}
		vmLock, err := directories.acquireVMLock(ctx, vmID)
		if err != nil {
			return false, appendInstanceOperationError(
				err,
				"close rejected VM runtime directory",
				roots.deps.close(context.WithoutCancel(ctx), runtimeFD),
			)
		}
		record, found, readErr := directories.readRecord(ctx, vmID)
		releaseErr := vmLock.Release(context.WithoutCancel(ctx))
		closeErr := roots.deps.close(context.WithoutCancel(ctx), runtimeFD)
		if readErr != nil {
			return false, appendInstanceOperationError(
				appendInstanceOperationError(readErr, "release scanned VM lock", releaseErr),
				"close scanned VM runtime directory",
				closeErr,
			)
		}
		if releaseErr != nil || closeErr != nil {
			return false, instanceAtomicError(
				"release scanned instance authority resources",
				errors.Join(releaseErr, closeErr),
			)
		}
		if !found {
			return false, instanceAtomicError("instance authority record disappeared while locked", nil)
		}
		if (record.lifecycle == instanceLifecycleRegistered || record.lifecycle == instanceLifecycleCleaning) &&
			record.release == release {
			return true, nil
		}
	}
	return false, nil
}

func isInstanceTempName(name string) bool {
	const prefix = ".mvm-instance-"
	const suffix = ".tmp"
	if !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, suffix) {
		return false
	}
	hexValue := strings.TrimSuffix(strings.TrimPrefix(name, prefix), suffix)
	if len(hexValue) != 32 {
		return false
	}
	for _, char := range hexValue {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}
