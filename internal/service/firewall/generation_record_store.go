package firewall

import (
	"context"
	"errors"
	"fmt"
	"maps"

	"golang.org/x/sys/unix"

	"mvmctl/pkg/errs"
)

const (
	maxGenerationCleanupDetailBytes = 512

	generationCleanupFailedDetailKey     = "firewall_generation_cleanup_failed"
	generationCleanupDiagnosticDetailKey = "firewall_generation_cleanup_detail"
)

func (store *generationRecordStore) read(
	ctx context.Context,
) (generationRecord, generationRecordFileEvidence, bool, error) {
	if store == nil || store.state == nil {
		return generationRecord{}, generationRecordFileEvidence{}, false, generationAuthorityError(
			"firewall generation record store is unavailable",
			nil,
			nil,
		)
	}
	state := store.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if !state.active {
		return generationRecord{}, generationRecordFileEvidence{}, false, generationAuthorityError(
			"firewall generation record store is released",
			nil,
			nil,
		)
	}
	return readGenerationRecord(ctx, state.deps, state.policy, state.directory)
}

func readGenerationRecord(
	ctx context.Context,
	deps generationRecordStoreDeps,
	policy generationRecordStorePolicy,
	directoryFD int,
) (record generationRecord, evidence generationRecordFileEvidence, found bool, returnErr error) {
	if err := ctx.Err(); err != nil {
		return generationRecord{}, generationRecordFileEvidence{}, false, generationAuthorityError(
			"open firewall generation authority record",
			err,
			nil,
		)
	}
	fd, err := deps.openAt2(ctx, directoryFD, generationRecordName, &unix.OpenHow{
		Flags:   generationRecordReadFlags,
		Resolve: generationRecordManagedResolve,
	})
	if errors.Is(err, unix.ENOENT) {
		if contextErr := ctx.Err(); contextErr != nil {
			return generationRecord{}, generationRecordFileEvidence{}, false, generationAuthorityError(
				"open firewall generation authority record",
				contextErr,
				nil,
			)
		}
		return generationRecord{}, generationRecordFileEvidence{}, false, nil
	}
	if err != nil {
		return generationRecord{}, generationRecordFileEvidence{}, false, generationAuthorityError(
			"open firewall generation authority record",
			err,
			nil,
		)
	}
	defer func() {
		if err := deps.close(context.WithoutCancel(ctx), fd); err != nil {
			returnErr = appendGenerationCleanupError(returnErr, "close firewall generation authority record", err)
		}
	}()

	before, err := inspectGenerationRecordFile(ctx, deps, fd, policy)
	if err != nil {
		return generationRecord{}, generationRecordFileEvidence{}, false, err
	}
	raw, err := readStableGenerationRecord(ctx, deps, fd, before.size)
	if err != nil {
		return generationRecord{}, generationRecordFileEvidence{}, false, err
	}
	after, err := inspectGenerationRecordFile(ctx, deps, fd, policy)
	if err != nil {
		return generationRecord{}, generationRecordFileEvidence{}, false, err
	}
	if before != after {
		return generationRecord{}, generationRecordFileEvidence{}, false, generationAuthorityError(
			"firewall generation authority record changed while being read",
			nil,
			nil,
		)
	}
	record, err = decodeGenerationRecord(raw)
	if err != nil {
		return generationRecord{}, generationRecordFileEvidence{}, false, err
	}
	return record, after, true, nil
}

func inspectGenerationRecordFile(
	ctx context.Context,
	deps generationRecordStoreDeps,
	fd int,
	policy generationRecordStorePolicy,
) (generationRecordFileEvidence, error) {
	var stat unix.Stat_t
	if err := deps.fstat(ctx, fd, &stat); err != nil {
		return generationRecordFileEvidence{}, generationAuthorityError(
			"inspect firewall generation authority record",
			err,
			nil,
		)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Nlink != 1 {
		return generationRecordFileEvidence{}, generationAuthorityError(
			"firewall generation authority record is not a one-link regular file",
			nil,
			nil,
		)
	}
	if stat.Uid != policy.expectedUID || stat.Gid != policy.expectedGID ||
		stat.Mode&07777 != generationRecordFileMode {
		return generationRecordFileEvidence{}, generationAuthorityError(
			"firewall generation authority record has unexpected owner or mode",
			nil,
			nil,
		)
	}
	if stat.Size <= 0 || stat.Size > maxGenerationRecordBytes {
		return generationRecordFileEvidence{}, generationAuthorityError(
			"firewall generation authority record size is invalid",
			nil,
			nil,
		)
	}
	return generationRecordFileEvidence{
		device: uint64(stat.Dev),
		inode:  stat.Ino,
		mode:   stat.Mode,
		uid:    stat.Uid,
		gid:    stat.Gid,
		nlink:  stat.Nlink,
		size:   stat.Size,
		mtime:  stat.Mtim,
		ctime:  stat.Ctim,
	}, nil
}

func readStableGenerationRecord(
	ctx context.Context,
	deps generationRecordStoreDeps,
	fd int,
	size int64,
) ([]byte, error) {
	raw := make([]byte, size)
	for offset := 0; offset < len(raw); {
		if err := ctx.Err(); err != nil {
			return nil, generationAuthorityError("read firewall generation authority record", err, nil)
		}
		end := min(offset+generationRecordIOChunk, len(raw))
		count, err := deps.pread(ctx, fd, raw[offset:end], int64(offset))
		if count < 0 || count > end-offset {
			return nil, generationAuthorityError(
				"read firewall generation authority record",
				fmt.Errorf("invalid positioned read count %d", count),
				nil,
			)
		}
		offset += count
		if err != nil && !errors.Is(err, unix.EINTR) {
			return nil, generationAuthorityError("read firewall generation authority record", err, nil)
		}
		if count == 0 && err == nil {
			return nil, generationAuthorityError("firewall generation authority record is truncated", nil, nil)
		}
	}
	probe := []byte{0}
	for {
		count, err := deps.pread(ctx, fd, probe, size)
		if errors.Is(err, unix.EINTR) {
			if contextErr := ctx.Err(); contextErr != nil {
				return nil, generationAuthorityError(
					"probe firewall generation authority record growth",
					contextErr,
					nil,
				)
			}
			continue
		}
		if err != nil {
			return nil, generationAuthorityError("probe firewall generation authority record growth", err, nil)
		}
		if count != 0 {
			return nil, generationAuthorityError("firewall generation authority record grew while being read", nil, nil)
		}
		return raw, nil
	}
}

func (store *generationRecordStore) create(
	ctx context.Context,
	record generationRecord,
) (generationRecordFileEvidence, error) {
	if store == nil || store.state == nil {
		return generationRecordFileEvidence{}, generationAuthorityError(
			"firewall generation record store is unavailable",
			nil,
			nil,
		)
	}
	state := store.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if !state.active {
		return generationRecordFileEvidence{}, generationAuthorityError(
			"firewall generation record store is released",
			nil,
			nil,
		)
	}
	raw, err := encodeGenerationRecord(record)
	if err != nil {
		return generationRecordFileEvidence{}, err
	}
	_, _, found, err := readGenerationRecord(ctx, state.deps, state.policy, state.directory)
	if err != nil {
		return generationRecordFileEvidence{}, err
	}
	if found {
		return generationRecordFileEvidence{}, generationAuthorityError(
			"firewall generation authority record already exists",
			nil,
			nil,
		)
	}

	tempName, tempFD, err := createGenerationRecordTemp(ctx, state.deps, state.directory)
	if err != nil {
		return generationRecordFileEvidence{}, err
	}
	tempOpen := true
	published := false
	abort := func(primary error) error {
		cleanupCtx := context.WithoutCancel(ctx)
		result := primary
		if tempOpen {
			if closeErr := state.deps.close(cleanupCtx, tempFD); closeErr != nil {
				result = appendGenerationCleanupError(result, "close incomplete firewall generation record", closeErr)
			}
			tempOpen = false
		}
		if !published {
			if unlinkErr := state.deps.unlinkAt(cleanupCtx, state.directory, tempName, 0); unlinkErr != nil {
				result = appendGenerationCleanupError(result, "remove incomplete firewall generation record", unlinkErr)
			}
		}
		return result
	}
	if err := writeAllGenerationRecord(ctx, state.deps, tempFD, raw); err != nil {
		return generationRecordFileEvidence{}, abort(err)
	}
	if err := state.deps.fchown(ctx, tempFD, int(state.policy.expectedUID), int(state.policy.expectedGID)); err != nil {
		return generationRecordFileEvidence{}, abort(generationAuthorityError(
			"set firewall generation record owner",
			err,
			nil,
		))
	}
	if err := state.deps.fchmod(ctx, tempFD, generationRecordFileMode); err != nil {
		return generationRecordFileEvidence{}, abort(generationAuthorityError(
			"set firewall generation record mode",
			err,
			nil,
		))
	}
	beforeSync, err := inspectGenerationRecordFile(ctx, state.deps, tempFD, state.policy)
	if err != nil {
		return generationRecordFileEvidence{}, abort(err)
	}
	if beforeSync.size != int64(len(raw)) {
		return generationRecordFileEvidence{}, abort(generationAuthorityError(
			"firewall generation temporary record is incomplete",
			nil,
			nil,
		))
	}
	if err := state.deps.fsync(ctx, tempFD); err != nil {
		return generationRecordFileEvidence{}, abort(generationAuthorityError(
			"sync firewall generation temporary record",
			err,
			nil,
		))
	}
	afterSync, err := inspectGenerationRecordFile(ctx, state.deps, tempFD, state.policy)
	if err != nil {
		return generationRecordFileEvidence{}, abort(err)
	}
	if beforeSync != afterSync {
		return generationRecordFileEvidence{}, abort(generationAuthorityError(
			"firewall generation temporary record changed while syncing",
			nil,
			nil,
		))
	}
	tempOpen = false
	if err := state.deps.close(context.WithoutCancel(ctx), tempFD); err != nil {
		return generationRecordFileEvidence{}, abort(generationAuthorityError(
			"close firewall generation temporary record",
			err,
			nil,
		))
	}
	if err := ctx.Err(); err != nil {
		return generationRecordFileEvidence{}, abort(generationAuthorityError(
			"publish firewall generation authority record",
			err,
			nil,
		))
	}
	if err := state.deps.renameNoReplace(
		ctx,
		state.directory,
		tempName,
		state.directory,
		generationRecordName,
	); err != nil {
		return generationRecordFileEvidence{}, abort(generationAuthorityError(
			"atomically create firewall generation authority record",
			err,
			nil,
		))
	}
	published = true
	if err := state.deps.fsync(ctx, state.directory); err != nil {
		primary := annotateGenerationRecordCreation(
			generationAuthorityError("sync firewall generation authority directory", err, nil),
			true,
		)
		readBack, proof, found, readErr := readGenerationRecord(ctx, state.deps, state.policy, state.directory)
		if readErr != nil {
			return generationRecordFileEvidence{}, appendGenerationCleanupError(
				primary,
				"read created firewall generation authority record",
				readErr,
			)
		}
		if !found || !generationRecordsEqual(readBack, record) {
			return generationRecordFileEvidence{}, appendGenerationCleanupError(
				primary,
				"verify created firewall generation authority record",
				errors.New("record readback is inconsistent"),
			)
		}
		return proof, primary
	}
	readBack, proof, found, err := readGenerationRecord(ctx, state.deps, state.policy, state.directory)
	if err != nil {
		return generationRecordFileEvidence{}, annotateGenerationRecordCreation(err, false)
	}
	if !found || !generationRecordsEqual(readBack, record) {
		return generationRecordFileEvidence{}, annotateGenerationRecordCreation(
			generationAuthorityError("firewall generation authority readback is inconsistent", nil, nil),
			false,
		)
	}
	return proof, nil
}

func (store *generationRecordStore) replace(
	ctx context.Context,
	expected generationRecord,
	expectedEvidence generationRecordFileEvidence,
	replacement generationRecord,
) (generationRecordFileEvidence, error) {
	if store == nil || store.state == nil {
		return generationRecordFileEvidence{}, generationAuthorityError(
			"firewall generation record store is unavailable",
			nil,
			nil,
		)
	}
	state := store.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if !state.active {
		return generationRecordFileEvidence{}, generationAuthorityError(
			"firewall generation record store is released",
			nil,
			nil,
		)
	}
	if err := validateGenerationRecord(expected); err != nil {
		return generationRecordFileEvidence{}, err
	}
	raw, err := encodeGenerationRecord(replacement)
	if err != nil {
		return generationRecordFileEvidence{}, err
	}

	tempName, tempFD, err := createGenerationRecordTemp(ctx, state.deps, state.directory)
	if err != nil {
		return generationRecordFileEvidence{}, err
	}
	tempOpen := true
	replaced := false
	abort := func(primary error) error {
		cleanupCtx := context.WithoutCancel(ctx)
		result := primary
		if tempOpen {
			if closeErr := state.deps.close(cleanupCtx, tempFD); closeErr != nil {
				result = appendGenerationCleanupError(
					result,
					"close incomplete firewall generation replacement",
					closeErr,
				)
			}
			tempOpen = false
		}
		if !replaced {
			if unlinkErr := state.deps.unlinkAt(cleanupCtx, state.directory, tempName, 0); unlinkErr != nil {
				result = appendGenerationCleanupError(
					result,
					"remove incomplete firewall generation replacement",
					unlinkErr,
				)
			}
		}
		return result
	}
	if err := writeAllGenerationRecord(ctx, state.deps, tempFD, raw); err != nil {
		return generationRecordFileEvidence{}, abort(err)
	}
	if err := state.deps.fchown(ctx, tempFD, int(state.policy.expectedUID), int(state.policy.expectedGID)); err != nil {
		return generationRecordFileEvidence{}, abort(generationAuthorityError(
			"set firewall generation replacement owner",
			err,
			nil,
		))
	}
	if err := state.deps.fchmod(ctx, tempFD, generationRecordFileMode); err != nil {
		return generationRecordFileEvidence{}, abort(generationAuthorityError(
			"set firewall generation replacement mode",
			err,
			nil,
		))
	}
	beforeSync, err := inspectGenerationRecordFile(ctx, state.deps, tempFD, state.policy)
	if err != nil {
		return generationRecordFileEvidence{}, abort(err)
	}
	if beforeSync.size != int64(len(raw)) {
		return generationRecordFileEvidence{}, abort(generationAuthorityError(
			"firewall generation replacement record is incomplete",
			nil,
			nil,
		))
	}
	if err := state.deps.fsync(ctx, tempFD); err != nil {
		return generationRecordFileEvidence{}, abort(generationAuthorityError(
			"sync firewall generation replacement record",
			err,
			nil,
		))
	}
	afterSync, err := inspectGenerationRecordFile(ctx, state.deps, tempFD, state.policy)
	if err != nil {
		return generationRecordFileEvidence{}, abort(err)
	}
	if beforeSync != afterSync {
		return generationRecordFileEvidence{}, abort(generationAuthorityError(
			"firewall generation replacement record changed while syncing",
			nil,
			nil,
		))
	}
	tempOpen = false
	if err := state.deps.close(context.WithoutCancel(ctx), tempFD); err != nil {
		return generationRecordFileEvidence{}, abort(generationAuthorityError(
			"close firewall generation replacement record",
			err,
			nil,
		))
	}

	// CRITICAL: The exact record and inode evidence are re-admitted immediately before replacement. A changed target
	// is never overwritten merely because the caller still holds the global firewall lock.
	current, currentEvidence, found, err := readGenerationRecord(
		ctx,
		state.deps,
		state.policy,
		state.directory,
	)
	if err != nil {
		return generationRecordFileEvidence{}, abort(err)
	}
	if !found || !generationRecordsEqual(current, expected) || currentEvidence != expectedEvidence {
		return generationRecordFileEvidence{}, abort(generationAuthorityError(
			"firewall generation authority changed before replacement",
			nil,
			nil,
		))
	}
	if err := ctx.Err(); err != nil {
		return generationRecordFileEvidence{}, abort(generationAuthorityError(
			"replace firewall generation authority record",
			err,
			nil,
		))
	}
	if err := state.deps.renameReplace(
		ctx,
		state.directory,
		tempName,
		state.directory,
		generationRecordName,
	); err != nil {
		return generationRecordFileEvidence{}, abort(generationAuthorityError(
			"atomically replace firewall generation authority record",
			err,
			nil,
		))
	}
	replaced = true
	postCommitCtx := context.WithoutCancel(ctx)

	if err := state.deps.fsync(postCommitCtx, state.directory); err != nil {
		primary := annotateGenerationRecordReplacement(
			generationAuthorityError("sync firewall generation authority directory", err, nil),
			true,
		)
		proof, proofErr := readBackGenerationRecordReplacement(
			postCommitCtx,
			state.deps,
			state.policy,
			state.directory,
			replacement,
		)
		if proofErr != nil {
			return generationRecordFileEvidence{}, appendGenerationCleanupError(
				primary,
				"verify replaced firewall generation authority record",
				proofErr,
			)
		}
		return proof, primary
	}
	proof, err := readBackGenerationRecordReplacement(
		postCommitCtx,
		state.deps,
		state.policy,
		state.directory,
		replacement,
	)
	if err != nil {
		return generationRecordFileEvidence{}, annotateGenerationRecordReplacement(err, false)
	}
	return proof, nil
}

func readBackGenerationRecordReplacement(
	ctx context.Context,
	deps generationRecordStoreDeps,
	policy generationRecordStorePolicy,
	directoryFD int,
	replacement generationRecord,
) (generationRecordFileEvidence, error) {
	readBack, proof, found, err := readGenerationRecord(ctx, deps, policy, directoryFD)
	if err != nil {
		return generationRecordFileEvidence{}, err
	}
	if !found || !generationRecordsEqual(readBack, replacement) {
		return generationRecordFileEvidence{}, generationAuthorityError(
			"firewall generation authority replacement outcome is ambiguous",
			nil,
			nil,
		)
	}
	return proof, nil
}

func createGenerationRecordTemp(
	ctx context.Context,
	deps generationRecordStoreDeps,
	directoryFD int,
) (string, int, error) {
	for range generationRecordTempAttempts {
		if err := ctx.Err(); err != nil {
			return "", -1, generationAuthorityError("create firewall generation temporary record", err, nil)
		}
		name, err := deps.randomName(ctx)
		if err != nil {
			return "", -1, generationAuthorityError("generate firewall generation temporary name", err, nil)
		}
		if !generationRecordTempNamePattern.MatchString(name) {
			return "", -1, generationAuthorityError("firewall generation temporary name is invalid", nil, nil)
		}
		fd, err := deps.openAt2(ctx, directoryFD, name, &unix.OpenHow{
			Flags:   generationRecordTempFlags,
			Mode:    uint64(generationRecordFileMode),
			Resolve: generationRecordManagedResolve,
		})
		if errors.Is(err, unix.EEXIST) {
			continue
		}
		if err != nil {
			return "", -1, generationAuthorityError("create firewall generation temporary record", err, nil)
		}
		return name, fd, nil
	}
	return "", -1, generationAuthorityError("firewall generation temporary names exhausted", nil, nil)
}

func writeAllGenerationRecord(
	ctx context.Context,
	deps generationRecordStoreDeps,
	fd int,
	raw []byte,
) error {
	for written := 0; written < len(raw); {
		if err := ctx.Err(); err != nil {
			return generationAuthorityError("write firewall generation temporary record", err, nil)
		}
		count, err := deps.write(ctx, fd, raw[written:])
		if count < 0 || count > len(raw)-written {
			return generationAuthorityError(
				"write firewall generation temporary record",
				fmt.Errorf("invalid write count %d", count),
				nil,
			)
		}
		written += count
		if err != nil {
			return generationAuthorityError("write firewall generation temporary record", err, nil)
		}
		if count == 0 {
			return generationAuthorityError("firewall generation temporary record write made no progress", nil, nil)
		}
	}
	return nil
}

// CRITICAL: Cleanup diagnostics must not mutate or reclassify the authoritative operation failure.
func appendGenerationCleanupError(primary error, stage string, cause error) error {
	if cause == nil {
		return primary
	}
	detail := boundedGenerationCleanupDetail(stage, cause)
	if primary == nil {
		return newGenerationCleanupError(detail, cause)
	}
	domainErr := errs.AsDomainError(primary)
	if domainErr == nil {
		return newGenerationCleanupError(detail, errors.Join(primary, cause))
	}
	result := *domainErr
	result.Details = maps.Clone(domainErr.Details)
	if result.Details == nil {
		result.Details = make(map[string]any)
	}
	if _, exists := result.Details[generationCleanupFailedDetailKey]; !exists {
		result.Details[generationCleanupFailedDetailKey] = true
	}
	if _, exists := result.Details[generationCleanupDiagnosticDetailKey]; !exists {
		result.Details[generationCleanupDiagnosticDetailKey] = detail
	}
	result.Err = errors.Join(primary, cause)
	return &result
}

func generationCleanupError(message string, cause error) *errs.DomainError {
	return newGenerationCleanupError(boundedGenerationCleanupDetail(message, cause), cause)
}

func newGenerationCleanupError(detail string, cause error) *errs.DomainError {
	result := generationAuthorityError(detail, cause, nil)
	result.Details = map[string]any{
		generationCleanupFailedDetailKey:     true,
		generationCleanupDiagnosticDetailKey: detail,
	}
	return result
}

func boundedGenerationCleanupDetail(stage string, cause error) string {
	detail := stage
	if cause != nil {
		detail += ": " + cause.Error()
	}
	if len(detail) <= maxGenerationCleanupDetailBytes {
		return detail
	}
	return detail[:maxGenerationCleanupDetailBytes-3] + "..."
}

func annotateGenerationRecordCreation(err error, durabilityUncertain bool) *errs.DomainError {
	domainErr := errs.AsDomainError(err)
	if domainErr == nil {
		domainErr = generationAuthorityError("firewall generation authority creation failed", err, nil)
	}
	result := *domainErr
	result.Details = maps.Clone(domainErr.Details)
	if result.Details == nil {
		result.Details = make(map[string]any)
	}
	result.Details["record_created"] = true
	if durabilityUncertain {
		result.Details["durability_uncertain"] = true
	}
	result.Err = err
	return &result
}

func annotateGenerationRecordReplacement(err error, durabilityUncertain bool) *errs.DomainError {
	domainErr := errs.AsDomainError(err)
	if domainErr == nil {
		domainErr = generationAuthorityError("firewall generation authority replacement failed", err, nil)
	}
	result := *domainErr
	result.Details = maps.Clone(domainErr.Details)
	if result.Details == nil {
		result.Details = make(map[string]any)
	}
	result.Details["record_replaced"] = true
	if durabilityUncertain {
		result.Details["durability_uncertain"] = true
	}
	result.Err = err
	return &result
}
