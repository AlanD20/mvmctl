package jailer

import (
	"context"
	"errors"
	"strings"

	"golang.org/x/sys/unix"
)

// requireRetiredGuestControlWorkers is read-only under exclusive host admission and the instance index.
// Reset never drops retained root worker authority merely because its VM is already cleaned.
func (guard *HostResetGuard) requireRetiredGuestControlWorkers(ctx context.Context) (returnErr error) {
	roots := guard.state.roots
	parent := roots.retained[0]
	retained := []int{}
	defer func() {
		returnErr = errors.Join(returnErr, closeInstanceFDs(context.WithoutCancel(ctx), roots.deps, retained))
	}()
	for i, name := range []string{"var", "lib", "mvmctl", "guest-control"} {
		class := instanceAncestorDirectory
		if i == 2 {
			class = instanceStateBaseDirectory
		}
		if i == 3 {
			class = instancePrivateDirectory
		}
		next, err := openInstanceDirectory(ctx, roots.deps, parent, name, false, class, roots.policy)
		if errors.Is(err, unix.ENOENT) {
			return nil
		}
		if err != nil {
			return err
		}
		retained = append(retained, next)
		parent = next
	}
	owners, err := roots.deps.readDirNames(ctx, parent)
	if err != nil {
		return err
	}
	for _, name := range owners {
		uid, valid := validateCanonicalUIDName(name)
		if !valid {
			return instanceValidationError("worker reset owner is noncanonical")
		}
		directory, err := openInstanceDirectory(
			ctx,
			roots.deps,
			parent,
			name,
			false,
			instancePrivateDirectory,
			roots.policy,
		)
		if err != nil {
			return err
		}
		retained = append(retained, directory)
		names, err := roots.deps.readDirNames(ctx, directory)
		if err != nil {
			return err
		}
		for _, leaf := range names {
			if isInstanceTempName(leaf) {
				return instanceValidationError("worker reset has unfinished publication")
			}
			ackVM, isAck := strings.CutSuffix(leaf, ".aliases.json")
			vmID, isRecord := strings.CutSuffix(leaf, ".json")
			if isAck {
				vmID, isRecord = ackVM, true
			}
			if !isRecord {
				var validLock bool
				vmID, validLock = strings.CutSuffix(leaf, ".lock")
				if !validLock || !vmIDPattern.MatchString(vmID) {
					return instanceValidationError("worker reset has unexpected entry")
				}
				fd, err := roots.deps.openAt(ctx, directory, leaf, instanceReadFlags, 0)
				if err != nil {
					return err
				}
				retained = append(retained, fd)
				if err := verifyGuestControlWorkerFile(
					ctx,
					roots.deps,
					fd,
					"worker reset lock",
					roots.policy,
				); err != nil {
					return err
				}
				continue
			}
			if !vmIDPattern.MatchString(vmID) {
				return instanceValidationError("worker reset record name is invalid")
			}
			store := &guestControlWorkerStore{
				directories: &instanceUIDDirectories{
					deps:    roots.deps,
					policy:  roots.policy,
					uid:     uid,
					stateFD: directory,
				},
				vmID: vmID,
			}
			record, _, found, err := store.read(ctx)
			if err != nil {
				return err
			}
			if isAck {
				receipt, ackFound, ackErr := store.readAliasAcknowledgement(ctx)
				if ackErr != nil {
					return ackErr
				}
				if !found || !ackFound || receipt.WorkerGeneration > record.Generation ||
					receipt.WorkerGeneration == record.Generation && receipt.Principal != record.principal() {
					return instanceValidationError("worker reset alias ACK has no matching worker history")
				}
			}
			if !found || record.Phase != "stopped" {
				return instanceStateError(vmID, "host reset requires every Guest Control worker retired")
			}
			fd, absent, err := reopenGuestControlWorkerProcess(ctx, record)
			if err != nil {
				return err
			}
			if !absent {
				return errors.Join(
					instanceStateError(vmID, "host reset found a live Guest Control worker"),
					unix.Close(fd),
				)
			}
		}
	}
	return nil
}
