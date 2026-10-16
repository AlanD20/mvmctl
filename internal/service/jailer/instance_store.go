package jailer

import (
	"context"
	"errors"
	"fmt"

	"golang.org/x/sys/unix"

	"mvmctl/pkg/errs"
)

func (directories *instanceUIDDirectories) readRecord(
	ctx context.Context,
	vmID string,
) (record instanceRecord, found bool, returnErr error) {
	if !vmIDPattern.MatchString(vmID) {
		return instanceRecord{}, false, instanceValidationError("vm ID is invalid")
	}
	fd, err := directories.deps.openAt(ctx, directories.stateFD, vmID+".json", instanceReadFlags, 0)
	if errors.Is(err, unix.ENOENT) {
		return instanceRecord{}, false, nil
	}
	if err != nil {
		return instanceRecord{}, false, instanceAtomicError("open instance authority record", err)
	}
	defer func() {
		if err := directories.deps.close(context.WithoutCancel(ctx), fd); err != nil {
			returnErr = joinInstanceAtomicError(returnErr, "close instance authority record", err)
		}
	}()
	if err := verifyInstanceRegularFile(
		ctx,
		directories.deps,
		fd,
		"instance authority record",
		directories.policy,
	); err != nil {
		return instanceRecord{}, false, err
	}
	raw, err := readAllInstanceRecord(ctx, directories.deps, fd)
	if err != nil {
		return instanceRecord{}, false, instanceAtomicError("read instance authority record", err)
	}
	record, err = decodeInstanceRecord(raw)
	if err != nil {
		return instanceRecord{}, false, err
	}
	if record.ownerUID != directories.uid || record.vmID != vmID {
		return instanceRecord{}, false, instanceAtomicError("instance authority record identity is inconsistent", nil)
	}
	return record, true, nil
}

func (directories *instanceUIDDirectories) writeRecord(ctx context.Context, record instanceRecord) error {
	if record.ownerUID != directories.uid {
		return instanceAtomicError("instance authority record owner does not match directory", nil)
	}
	raw, err := encodeInstanceRecord(record)
	if err != nil {
		return err
	}
	tempName, tempFD, err := createInstanceRecordTemp(ctx, directories)
	if err != nil {
		return err
	}
	tempOpen := true
	abort := func(cause error) error {
		return abortInstanceRecordWrite(ctx, directories, tempFD, tempName, tempOpen, cause)
	}
	if err := writeAllInstanceRecord(ctx, directories.deps, tempFD, raw); err != nil {
		return abort(instanceAtomicError("write instance authority temporary record", err))
	}
	if err := directories.deps.fchown(
		ctx,
		tempFD,
		int(directories.policy.expectedUID),
		int(directories.policy.expectedGID),
	); err != nil {
		return abort(instanceAtomicError("set instance authority record owner", err))
	}
	if err := directories.deps.fchmod(ctx, tempFD, instanceStateFileMode); err != nil {
		return abort(instanceAtomicError("set instance authority record mode", err))
	}
	if err := verifyInstanceRegularFile(
		ctx,
		directories.deps,
		tempFD,
		"instance authority temporary record",
		directories.policy,
	); err != nil {
		return abort(err)
	}
	var tempStat unix.Stat_t
	if err := directories.deps.fstat(ctx, tempFD, &tempStat); err != nil {
		return abort(instanceAtomicError("inspect instance authority temporary record size", err))
	}
	if tempStat.Size != int64(len(raw)) {
		return abort(instanceAtomicError("instance authority temporary record size is incomplete", nil))
	}
	if err := directories.deps.fsync(ctx, tempFD); err != nil {
		return abort(instanceAtomicError("sync instance authority temporary record", err))
	}
	if err := directories.deps.close(context.WithoutCancel(ctx), tempFD); err != nil {
		tempOpen = false
		return abort(instanceAtomicError("close instance authority temporary record", err))
	}
	tempOpen = false
	if err := ctx.Err(); err != nil {
		return abort(instanceAtomicError("replace instance authority record", err))
	}
	if err := directories.inspectRecordTarget(ctx, record.vmID); err != nil {
		return abort(err)
	}
	if err := directories.deps.renameAt(
		ctx,
		directories.stateFD,
		tempName,
		directories.stateFD,
		record.vmID+".json",
	); err != nil {
		return abort(instanceAtomicError("atomically replace instance authority record", err))
	}
	if err := directories.deps.fsync(ctx, directories.stateFD); err != nil {
		return annotateInstanceRecordReplacement(
			instanceAtomicError("sync instance authority record directory", err),
			true,
		)
	}
	return nil
}

func (directories *instanceUIDDirectories) inspectRecordTarget(
	ctx context.Context,
	vmID string,
) (returnErr error) {
	fd, err := directories.deps.openAt(ctx, directories.stateFD, vmID+".json", instanceInspectFlags, 0)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil {
		return instanceAtomicError("open existing instance authority record", err)
	}
	defer func() {
		if err := directories.deps.close(context.WithoutCancel(ctx), fd); err != nil {
			returnErr = joinInstanceAtomicError(returnErr, "close existing instance authority record", err)
		}
	}()
	return verifyInstanceRegularFile(
		ctx,
		directories.deps,
		fd,
		"existing instance authority record",
		directories.policy,
	)
}

func createInstanceRecordTemp(
	ctx context.Context,
	directories *instanceUIDDirectories,
) (string, int, error) {
	for range instanceTempAttempts {
		if err := ctx.Err(); err != nil {
			return "", -1, instanceAtomicError("create instance authority temporary record", err)
		}
		name, err := directories.deps.randomName(ctx)
		if err != nil {
			return "", -1, instanceAtomicError("generate instance authority temporary name", err)
		}
		fd, err := directories.deps.openAt(
			ctx,
			directories.stateFD,
			name,
			instanceTempFlags,
			instanceStateFileMode,
		)
		if errors.Is(err, unix.EEXIST) {
			continue
		}
		if err != nil {
			return "", -1, instanceAtomicError("create instance authority temporary record", err)
		}
		return name, fd, nil
	}
	return "", -1, instanceAtomicError("exclusive instance authority temporary names exhausted", nil)
}

func writeAllInstanceRecord(
	ctx context.Context,
	deps instanceAuthorityDeps,
	fd int,
	value []byte,
) error {
	for written := 0; written < len(value); {
		if err := ctx.Err(); err != nil {
			return err
		}
		count, err := deps.write(ctx, fd, value[written:])
		if count < 0 || count > len(value)-written {
			return fmt.Errorf("invalid write count %d", count)
		}
		written += count
		if err != nil {
			return err
		}
		if count == 0 {
			return fmt.Errorf("zero-length instance authority write")
		}
	}
	return nil
}

func abortInstanceRecordWrite(
	ctx context.Context,
	directories *instanceUIDDirectories,
	tempFD int,
	tempName string,
	tempOpen bool,
	cause error,
) error {
	cleanupCtx := context.WithoutCancel(ctx)
	result := cause
	if tempOpen {
		if err := directories.deps.close(cleanupCtx, tempFD); err != nil {
			result = joinInstanceAtomicError(result, "close incomplete instance authority record", err)
		}
	}
	if err := directories.deps.unlinkAt(cleanupCtx, directories.stateFD, tempName, 0); err != nil {
		result = joinInstanceAtomicError(result, "remove incomplete instance authority record", err)
	}
	return result
}

func annotateInstanceRecordReplacement(err error, durabilityUncertain bool) *errs.DomainError {
	domainErr := errs.AsDomainError(err)
	if domainErr == nil {
		domainErr = instanceAtomicError("instance authority record replacement", err)
	}
	if domainErr.Details == nil {
		domainErr.Details = make(map[string]any)
	}
	domainErr.Details["record_replaced"] = true
	if durabilityUncertain {
		domainErr.Details["durability_uncertain"] = true
	}
	return domainErr
}
