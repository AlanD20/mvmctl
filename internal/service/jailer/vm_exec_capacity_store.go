package jailer

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"strings"

	"golang.org/x/sys/unix"

	"mvmctl/pkg/errs"
)

func (store *vmExecCapacityStore) readRecord(
	ctx context.Context,
	reservationID vmExecReservationID,
) (record vmExecCapacityRecord, found bool, returnErr error) {
	if !vmExecReservationIDPattern.MatchString(reservationID.value) {
		return vmExecCapacityRecord{}, false, vmExecCapacityValidationError("VM-exec reservation ID is invalid")
	}
	deps, policy, stateFD, err := store.snapshot()
	if err != nil {
		return vmExecCapacityRecord{}, false, err
	}
	if err := ctx.Err(); err != nil {
		return vmExecCapacityRecord{}, false, vmExecCapacityEffectError("open VM-exec capacity record", err)
	}
	fd, err := deps.openAt2(ctx, stateFD, reservationID.value+".json", &unix.OpenHow{
		Flags: vmExecCapacityReadFlags, Resolve: vmExecCapacityResolve,
	})
	if errors.Is(err, unix.ENOENT) {
		if contextErr := ctx.Err(); contextErr != nil {
			return vmExecCapacityRecord{}, false, vmExecCapacityEffectError(
				"open VM-exec capacity record",
				contextErr,
			)
		}
		return vmExecCapacityRecord{}, false, nil
	}
	if err != nil {
		return vmExecCapacityRecord{}, false, vmExecCapacityEffectError("open VM-exec capacity record", err)
	}
	defer func() {
		if err := deps.close(context.WithoutCancel(ctx), fd); err != nil {
			returnErr = appendVMExecCapacityError(returnErr, "close VM-exec capacity record", err)
		}
	}()
	before, err := verifyVMExecCapacityRecordFile(ctx, deps, fd, policy)
	if err != nil {
		return vmExecCapacityRecord{}, false, err
	}
	raw, err := readAllVMExecCapacityRecord(ctx, deps, fd)
	if err != nil {
		return vmExecCapacityRecord{}, false, vmExecCapacityEffectError("read VM-exec capacity record", err)
	}
	after, err := verifyVMExecCapacityRecordFile(ctx, deps, fd, policy)
	if err != nil {
		return vmExecCapacityRecord{}, false, err
	}
	if !sameVMExecCapacityFile(before, after) || after.size != uint64(len(raw)) {
		return vmExecCapacityRecord{}, false, vmExecCapacityCorruptError(
			"VM-exec capacity record changed while being read",
			nil,
		)
	}
	record, err = decodeVMExecCapacityRecord(raw)
	if err != nil {
		return vmExecCapacityRecord{}, false, err
	}
	if record.reservationID != reservationID {
		return vmExecCapacityRecord{}, false, vmExecCapacityCorruptError(
			"VM-exec capacity record identity is inconsistent",
			nil,
		)
	}
	return record, true, nil
}

func (store *vmExecCapacityStore) createRecord(ctx context.Context, record vmExecCapacityRecord) error {
	if err := validateVMExecCapacityRecord(record); err != nil {
		return err
	}
	deps, policy, stateFD, err := store.snapshot()
	if err != nil {
		return err
	}
	raw, err := encodeVMExecCapacityRecord(record)
	if err != nil {
		return err
	}
	tempName, tempFD, err := createVMExecCapacityTemp(ctx, deps, policy, stateFD)
	if err != nil {
		return err
	}
	tempOpen := true
	abort := func(cause error) error {
		return abortVMExecCapacityWrite(ctx, deps, stateFD, tempFD, tempName, tempOpen, cause)
	}
	if err := writeAllVMExecCapacityRecord(ctx, deps, tempFD, raw); err != nil {
		return abort(vmExecCapacityEffectError("write VM-exec capacity temporary record", err))
	}
	if err := deps.fchown(ctx, tempFD, int(policy.expectedUID), int(policy.expectedGID)); err != nil {
		return abort(vmExecCapacityEffectError("set VM-exec capacity record owner", err))
	}
	if err := deps.fchmod(ctx, tempFD, vmExecCapacityRecordMode); err != nil {
		return abort(vmExecCapacityEffectError("set VM-exec capacity record mode", err))
	}
	before, err := verifyVMExecCapacityRecordFile(ctx, deps, tempFD, policy)
	if err != nil {
		return abort(err)
	}
	if before.size != uint64(len(raw)) {
		return abort(vmExecCapacityCorruptError("VM-exec capacity temporary record is incomplete", nil))
	}
	if err := deps.fsync(ctx, tempFD); err != nil {
		return abort(vmExecCapacityEffectError("sync VM-exec capacity temporary record", err))
	}
	after, err := verifyVMExecCapacityRecordFile(ctx, deps, tempFD, policy)
	if err != nil {
		return abort(err)
	}
	if !sameVMExecCapacityFile(before, after) {
		return abort(vmExecCapacityCorruptError("VM-exec capacity temporary record changed while syncing", nil))
	}
	tempOpen = false
	if err := deps.close(context.WithoutCancel(ctx), tempFD); err != nil {
		return abort(vmExecCapacityEffectError("close VM-exec capacity temporary record", err))
	}
	if err := ctx.Err(); err != nil {
		return abort(vmExecCapacityEffectError("publish VM-exec capacity record", err))
	}
	if err := deps.renameNoReplace(
		ctx,
		stateFD,
		tempName,
		stateFD,
		record.reservationID.value+".json",
	); err != nil {
		return abort(vmExecCapacityEffectError("atomically create VM-exec capacity record", err))
	}
	if err := deps.fsync(ctx, stateFD); err != nil {
		result := vmExecCapacityEffectError("sync VM-exec capacity record directory", err)
		result.Details = map[string]any{"record_created": true, "durability_uncertain": true}
		return result
	}
	return nil
}

func (store *vmExecCapacityStore) scanAndPrune(
	ctx context.Context,
	hostBootID string,
	boottimeNS uint64,
) ([]vmExecCapacityRecord, error) {
	if !hostBootIDPattern.MatchString(hostBootID) {
		return nil, vmExecCapacityCorruptError("current host boot identity is invalid", nil)
	}
	deps, _, stateFD, err := store.snapshot()
	if err != nil {
		return nil, err
	}
	names, err := deps.readDirNames(ctx, stateFD)
	if err != nil {
		return nil, vmExecCapacityEffectError("enumerate VM-exec capacity records", err)
	}
	active := make([]vmExecCapacityRecord, 0, len(names))
	crashTemps := make([]string, 0)
	prune := make([]vmExecReservationID, 0)
	pruned := false
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return nil, vmExecCapacityEffectError("enumerate VM-exec capacity records", err)
		}
		if vmExecCapacityTempNamePattern.MatchString(name) {
			if err := store.admitSafeCrashTemp(ctx, name); err != nil {
				return nil, err
			}
			crashTemps = append(crashTemps, name)
			continue
		}
		if !vmExecCapacityRecordNamePattern.MatchString(name) {
			return nil, vmExecCapacityCorruptError("VM-exec capacity directory contains an ambiguous entry", nil)
		}
		reservationID, idErr := newVMExecReservationID(strings.TrimSuffix(name, ".json"))
		if idErr != nil {
			return nil, vmExecCapacityCorruptError("VM-exec capacity record name is invalid", idErr)
		}
		record, found, readErr := store.readRecord(ctx, reservationID)
		if readErr != nil {
			return nil, readErr
		}
		if !found {
			return nil, vmExecCapacityCorruptError("VM-exec capacity record disappeared during scan", nil)
		}
		if record.hostBootID != hostBootID || record.expiresBoottimeNS <= boottimeNS {
			prune = append(prune, reservationID)
			continue
		}
		if record.createdBoottimeNS > boottimeNS {
			return nil, vmExecCapacityCorruptError("VM-exec capacity record begins in the future", nil)
		}
		active = append(active, record)
	}
	for _, name := range crashTemps {
		if err := deps.unlinkAt(ctx, stateFD, name, 0); err != nil {
			return nil, vmExecCapacityEffectError("prune crashed VM-exec capacity temporary record", err)
		}
		pruned = true
	}
	for _, reservationID := range prune {
		if err := deps.unlinkAt(ctx, stateFD, reservationID.value+".json", 0); err != nil {
			return nil, vmExecCapacityEffectError("prune VM-exec capacity record", err)
		}
		pruned = true
	}
	if pruned {
		if err := deps.fsync(ctx, stateFD); err != nil {
			result := vmExecCapacityEffectError("sync pruned VM-exec capacity records", err)
			result.Details = map[string]any{"records_pruned": true, "durability_uncertain": true}
			return nil, result
		}
	}
	return active, nil
}

// admitSafeCrashTemp admits only the exact temporary name and metadata shape that this store creates. Deletion is
// deferred until the complete directory scan proves there is no other corrupt or ambiguous entry.
func (store *vmExecCapacityStore) admitSafeCrashTemp(
	ctx context.Context,
	name string,
) (returnErr error) {
	if !vmExecCapacityTempNamePattern.MatchString(name) {
		return vmExecCapacityCorruptError("VM-exec capacity temporary record name is invalid", nil)
	}
	deps, policy, stateFD, err := store.snapshot()
	if err != nil {
		return err
	}
	fd, err := deps.openAt2(ctx, stateFD, name, &unix.OpenHow{
		Flags: vmExecCapacityReadFlags, Resolve: vmExecCapacityResolve,
	})
	if err != nil {
		return vmExecCapacityEffectError("open crashed VM-exec capacity temporary record", err)
	}
	defer func() {
		if err := deps.close(context.WithoutCancel(ctx), fd); err != nil {
			returnErr = appendVMExecCapacityError(returnErr, "close crashed VM-exec capacity temporary record", err)
		}
	}()
	evidence, err := inspectVMExecCapacityFile(ctx, deps, fd)
	if err != nil {
		return vmExecCapacityEffectError("inspect crashed VM-exec capacity temporary record", err)
	}
	if evidence.mode&unix.S_IFMT != unix.S_IFREG || evidence.links != 1 ||
		evidence.uid != policy.expectedUID || evidence.gid != policy.expectedGID ||
		evidence.mode&07777 != uint16(vmExecCapacityRecordMode) || evidence.size > maxVMExecCapacityRecordBytes {
		return vmExecCapacityCorruptError("crashed VM-exec capacity temporary record has unsafe metadata", nil)
	}
	return nil
}

func (store *vmExecCapacityStore) removeRecord(
	ctx context.Context,
	reservationID vmExecReservationID,
) (bool, error) {
	record, found, err := store.readRecord(ctx, reservationID)
	if err != nil || !found {
		return false, err
	}
	if record.reservationID != reservationID {
		return false, vmExecCapacityCorruptError("VM-exec capacity record identity is inconsistent", nil)
	}
	deps, _, stateFD, err := store.snapshot()
	if err != nil {
		return false, err
	}
	if err := deps.unlinkAt(ctx, stateFD, reservationID.value+".json", 0); err != nil {
		return false, vmExecCapacityEffectError("remove VM-exec capacity record", err)
	}
	if err := deps.fsync(ctx, stateFD); err != nil {
		result := vmExecCapacityEffectError("sync removed VM-exec capacity record", err)
		result.Details = map[string]any{"record_deleted": true, "durability_uncertain": true}
		return true, result
	}
	return true, nil
}

func createVMExecCapacityTemp(
	ctx context.Context,
	deps vmExecCapacityStoreDeps,
	policy vmExecCapacityStorePolicy,
	stateFD int,
) (string, int, error) {
	for range vmExecCapacityTempAttempts {
		if err := ctx.Err(); err != nil {
			return "", -1, vmExecCapacityEffectError("create VM-exec capacity temporary record", err)
		}
		name, err := deps.randomTempName(ctx)
		if err != nil {
			return "", -1, vmExecCapacityEffectError("generate VM-exec capacity temporary name", err)
		}
		if !vmExecCapacityTempNamePattern.MatchString(name) {
			return "", -1, vmExecCapacityCorruptError("generated VM-exec capacity temporary name is invalid", nil)
		}
		fd, err := deps.openAt2(ctx, stateFD, name, &unix.OpenHow{
			Flags: vmExecCapacityTempFlags, Mode: uint64(vmExecCapacityRecordMode), Resolve: vmExecCapacityResolve,
		})
		if errors.Is(err, unix.EEXIST) {
			continue
		}
		if err != nil {
			return "", -1, vmExecCapacityEffectError("create VM-exec capacity temporary record", err)
		}
		return name, fd, nil
	}
	return "", -1, vmExecCapacityCorruptError("exclusive VM-exec capacity temporary names exhausted", nil)
}

func readAllVMExecCapacityRecord(ctx context.Context, deps vmExecCapacityStoreDeps, fd int) ([]byte, error) {
	result := make([]byte, 0, vmExecCapacityIOChunk)
	buffer := make([]byte, vmExecCapacityIOChunk)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		count, err := deps.read(ctx, fd, buffer)
		if count < 0 || count > len(buffer) {
			return nil, fmt.Errorf("invalid read count %d", count)
		}
		if len(result)+count > maxVMExecCapacityRecordBytes {
			return nil, errors.New("VM-exec capacity record exceeds size limit")
		}
		result = append(result, buffer[:count]...)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if count == 0 {
			return result, nil
		}
	}
}

func writeAllVMExecCapacityRecord(ctx context.Context, deps vmExecCapacityStoreDeps, fd int, value []byte) error {
	for written := 0; written < len(value); {
		if err := ctx.Err(); err != nil {
			return err
		}
		count, err := deps.write(ctx, fd, value[written:])
		if count < 0 || count > len(value)-written {
			return fmt.Errorf("invalid write count %d", count)
		}
		written += count
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return err
		}
		if count == 0 {
			return errors.New("zero-length VM-exec capacity write")
		}
	}
	return nil
}

func abortVMExecCapacityWrite(
	ctx context.Context,
	deps vmExecCapacityStoreDeps,
	stateFD int,
	tempFD int,
	tempName string,
	tempOpen bool,
	primary error,
) error {
	cleanupCtx := context.WithoutCancel(ctx)
	result := primary
	if tempOpen {
		if err := deps.close(cleanupCtx, tempFD); err != nil {
			result = appendVMExecCapacityError(result, "close incomplete VM-exec capacity record", err)
		}
	}
	if err := deps.unlinkAt(cleanupCtx, stateFD, tempName, 0); err != nil {
		result = appendVMExecCapacityError(result, "remove incomplete VM-exec capacity record", err)
	}
	return result
}

func appendVMExecCapacityError(primary error, diagnostic string, cause error) error {
	if cause == nil {
		return primary
	}
	if primary == nil {
		result := vmExecCapacityEffectError(diagnostic, cause)
		result.Details = map[string]any{"cleanup_required": true, "cleanup_diagnostic": diagnostic}
		return result
	}
	domainErr := errs.AsDomainError(primary)
	if domainErr == nil {
		result := vmExecCapacityEffectError(primary.Error(), errors.Join(primary, cause))
		result.Details = map[string]any{"cleanup_required": true, "cleanup_diagnostic": diagnostic}
		return result
	}
	result := *domainErr
	result.Details = maps.Clone(domainErr.Details)
	if result.Details == nil {
		result.Details = make(map[string]any, 2)
	}
	result.Details["cleanup_required"] = true
	if _, exists := result.Details["cleanup_diagnostic"]; !exists {
		result.Details["cleanup_diagnostic"] = diagnostic
	}
	result.Err = errors.Join(primary, cause)
	return &result
}
