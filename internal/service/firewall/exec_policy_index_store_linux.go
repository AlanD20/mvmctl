package firewall

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"

	"golang.org/x/sys/unix"

	"mvmctl/internal/lib/authorityfs"
	"mvmctl/internal/lib/model"
)

// AdmitVMExecPolicy is called after Jailer authenticates the live VM pair.
// Admission keeps the cross-process lock only for the bounded lookup.
func AdmitVMExecPolicy(
	ctx context.Context, identity model.VMToVMExecIdentity,
) (_ bool, returnErr error) {
	global, err := authorityfs.OpenFirewall(ctx)
	if err != nil {
		return false, err
	}
	cleanup := context.WithoutCancel(ctx)
	defer func() { returnErr = errors.Join(returnErr, global.Release(cleanup)) }()
	lock, err := global.Acquire(ctx)
	if err != nil {
		return false, err
	}
	defer func() { returnErr = errors.Join(returnErr, lock.Release(cleanup)) }()
	store, absence, err := openHostResetRecordStore(ctx)
	if err != nil {
		return false, err
	}
	if absence != nil {
		return false, closeGenerationRecordFDs(cleanup, absence.deps, absence.retained)
	}
	defer func() { returnErr = errors.Join(returnErr, store.Release(cleanup)) }()
	return store.execPolicyAllowed(
		ctx, identity.OwnerUID, identity.SourceVMID, identity.DestinationVMID, identity.TargetUser,
	)
}

func execPolicyIndexName(ownerUID uint32) string {
	return fmt.Sprintf("exec-policy-%d.bin", ownerUID)
}

type execIndexReader struct {
	ctx   context.Context
	state *generationRecordStoreState
	fd    int
}

func (reader execIndexReader) ReadAt(data []byte, offset int64) (int, error) {
	read := 0
	for read < len(data) {
		if err := reader.ctx.Err(); err != nil {
			return read, err
		}
		n, err := reader.state.deps.pread(reader.ctx, reader.fd, data[read:], offset+int64(read))
		if n < 0 || n > len(data)-read {
			return read, io.ErrUnexpectedEOF
		}
		read += n
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return read, err
		}
		if n == 0 {
			return read, io.EOF
		}
	}
	return read, nil
}

// CALLER MUST HOLD: state.mu and the host-global firewall lock.
func openExecPolicyIndex(
	ctx context.Context, state *generationRecordStoreState, ownerUID uint32,
) (execIndexReader, generationRecordFileEvidence, bool, error) {
	fd, err := state.deps.openAt2(ctx, state.directory, execPolicyIndexName(ownerUID), &unix.OpenHow{
		Flags: generationRecordReadFlags, Resolve: generationRecordManagedResolve,
	})
	if errors.Is(err, unix.ENOENT) {
		return execIndexReader{}, generationRecordFileEvidence{}, false, nil
	}
	if err != nil {
		return execIndexReader{}, generationRecordFileEvidence{}, false,
			preserveGenerationAuthorityError("open exec authorization index", err)
	}
	evidence, err := inspectPolicyAuthorityFile(ctx, state, fd, execPolicyProjectionBudget)
	reader := execIndexReader{ctx: ctx, state: state, fd: fd}
	if err == nil {
		_, err = readExecPolicyIndexHeader(reader, evidence.size, ownerUID)
	}
	if err != nil {
		return execIndexReader{}, generationRecordFileEvidence{}, false, errors.Join(err, reader.close(ctx))
	}
	return reader, evidence, true, nil
}

func (reader execIndexReader) close(ctx context.Context) error {
	return reader.state.deps.close(context.WithoutCancel(ctx), reader.fd)
}

func (store *generationRecordStore) execPolicyAllowed(
	ctx context.Context, ownerUID uint32, sourceVMID, destinationVMID, targetUser string,
) (_ bool, returnErr error) {
	state := store.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if !state.active {
		return false, invalidGeneration("exec authorization index store is released")
	}
	reader, evidence, found, err := openExecPolicyIndex(ctx, state, ownerUID)
	if err != nil || !found {
		return false, err
	}
	defer func() { returnErr = errors.Join(returnErr, reader.close(ctx)) }()
	allowed, err := lookupExecPolicyIndex(reader, evidence.size, ownerUID, sourceVMID, destinationVMID, targetUser)
	if err != nil || !allowed {
		return false, err
	}
	after, err := inspectPolicyAuthorityFile(ctx, state, reader.fd, execPolicyProjectionBudget)
	if err != nil {
		return false, err
	}
	if evidence != after {
		return false, invalidGeneration("exec authorization index changed during lookup")
	}
	return true, nil
}

func (store *generationRecordStore) execPolicyIndexState(
	ctx context.Context, ownerUID uint32,
) (_ uint32, _ bool, returnErr error) {
	state := store.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if !state.active {
		return 0, false, invalidGeneration("exec authorization index store is released")
	}
	reader, evidence, found, err := openExecPolicyIndex(ctx, state, ownerUID)
	if err != nil || !found {
		return 0, found, err
	}
	defer func() { returnErr = errors.Join(returnErr, reader.close(ctx)) }()
	header, err := readExecPolicyIndexHeader(reader, evidence.size, ownerUID)
	return header.status, true, err
}

func (store *generationRecordStore) writeExecPolicyIndex(
	ctx context.Context, ownerUID uint32, projection execPolicyProjection, committed bool,
) (returnErr error) {
	state := store.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if !state.active {
		return invalidGeneration("exec authorization index store is released")
	}
	name := execPolicyIndexName(ownerUID)
	raw := projection.encodeIndex(ownerUID, committed)
	if _, err := readExecPolicyIndexHeader(bytes.NewReader(raw), int64(len(raw)), ownerUID); err != nil {
		return err
	}
	current, evidence, exists, err := openExecPolicyIndex(ctx, state, ownerUID)
	if err != nil {
		return err
	}
	if exists {
		defer func() { returnErr = errors.Join(returnErr, current.close(ctx)) }()
	}

	tempName, fd, err := createGenerationRecordTemp(ctx, state.deps, state.directory)
	if err != nil {
		return err
	}
	cleanup := context.WithoutCancel(ctx)
	published := false
	defer func() {
		returnErr = errors.Join(returnErr, state.deps.close(cleanup, fd))
		if !published {
			returnErr = errors.Join(returnErr, state.deps.unlinkAt(cleanup, state.directory, tempName, 0))
		}
	}()
	if err := writeAllGenerationRecord(ctx, state.deps, fd, raw); err != nil {
		return err
	}
	if err := state.deps.fchown(ctx, fd, int(state.policy.expectedUID), int(state.policy.expectedGID)); err != nil {
		return err
	}
	if err := state.deps.fchmod(ctx, fd, generationRecordFileMode); err != nil {
		return err
	}
	if err := state.deps.fsync(ctx, fd); err != nil {
		return err
	}
	if exists {
		fresh, observed, found, observeErr := openExecPolicyIndex(ctx, state, ownerUID)
		if observeErr != nil {
			return observeErr
		}
		if !found {
			return invalidGeneration("exec authorization index disappeared before publication")
		}
		if err := fresh.close(ctx); err != nil {
			return err
		}
		if evidence != observed {
			return invalidGeneration("exec authorization index changed before publication")
		}
		err = state.deps.renameReplace(ctx, state.directory, tempName, state.directory, name)
	} else {
		err = state.deps.renameNoReplace(ctx, state.directory, tempName, state.directory, name)
	}
	if err != nil {
		return preserveGenerationAuthorityError("publish exec authorization index", err)
	}
	published = true
	if err := state.deps.fsync(cleanup, state.directory); err != nil {
		return preserveGenerationAuthorityError("sync exec authorization index directory", err)
	}
	reopened, actual, found, err := openExecPolicyIndex(cleanup, state, ownerUID)
	if err != nil || !found {
		return invalidGeneration("cannot verify published exec authorization index")
	}
	defer func() { returnErr = errors.Join(returnErr, reopened.close(cleanup)) }()
	written, err := inspectPolicyAuthorityFile(cleanup, state, fd, execPolicyProjectionBudget)
	if err != nil || written != actual {
		return invalidGeneration("published exec authorization index identity differs")
	}
	readback, err := readStableGenerationRecord(cleanup, state.deps, reopened.fd, actual.size)
	if err != nil || !bytes.Equal(readback, raw) {
		return invalidGeneration("published exec authorization index content differs")
	}
	return nil
}
