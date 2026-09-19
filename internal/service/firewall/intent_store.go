package firewall

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"

	"golang.org/x/sys/unix"
)

const (
	intentRecordName     = "intent.json"
	maxIntentRecordBytes = 16 * 1024 * 1024
)

// intentJournal retains both compiler inputs until the generation record determines the outcome.
// It never authorizes kernel mutation independently of that record and exact kernel observation.
type intentJournal struct {
	SchemaVersion uint32            `json:"schema_version"`
	BootID        string            `json:"boot_id"`
	HostNamespace NamespaceIdentity `json:"host_namespace"`
	Before        Intent            `json:"before"`
	Target        Intent            `json:"target"`
}

func decodeIntentJournal(raw []byte) (intentJournal, error) {
	var journal intentJournal
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&journal); err != nil {
		return intentJournal{}, generationAuthorityError("decode firewall intent journal", err, nil)
	}
	// CRITICAL: Only our exact canonical encoding is admitted. This also rejects duplicate keys,
	// case aliases, omitted fields, trailing input, and alternative scalar representations.
	canonical, err := json.Marshal(journal)
	if err != nil || !bytes.Equal(raw, canonical) || journal.SchemaVersion != 1 {
		return intentJournal{}, generationAuthorityError("firewall intent journal is not canonical", err, nil)
	}
	return journal, nil
}

func inspectIntentFile(
	ctx context.Context, state *generationRecordStoreState, fd int,
) (generationRecordFileEvidence, error) {
	var stat unix.Stat_t
	if err := state.deps.fstat(ctx, fd, &stat); err != nil {
		return generationRecordFileEvidence{}, generationAuthorityError("inspect firewall intent journal", err, nil)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Nlink != 1 ||
		stat.Uid != state.policy.expectedUID || stat.Gid != state.policy.expectedGID ||
		stat.Mode&07777 != generationRecordFileMode || stat.Size <= 0 || stat.Size > maxIntentRecordBytes {
		return generationRecordFileEvidence{}, generationAuthorityError(
			"firewall intent journal metadata is unsafe",
			nil,
			nil,
		)
	}
	return generationRecordFileEvidence{
		device: uint64(stat.Dev), inode: stat.Ino, mode: stat.Mode, uid: stat.Uid, gid: stat.Gid,
		nlink: stat.Nlink, size: stat.Size, mtime: stat.Mtim, ctime: stat.Ctim,
	}, nil
}

// CALLER MUST HOLD: state.mu and the host-global firewall mutation lock.
func readIntentFile(
	ctx context.Context, state *generationRecordStoreState,
) (raw []byte, evidence generationRecordFileEvidence, found bool, returnErr error) {
	if err := ctx.Err(); err != nil {
		return nil, evidence, false, err
	}
	fd, err := state.deps.openAt2(ctx, state.directory, intentRecordName, &unix.OpenHow{
		Flags: generationRecordReadFlags, Resolve: generationRecordManagedResolve,
	})
	if errors.Is(err, unix.ENOENT) {
		return nil, evidence, false, nil
	}
	if err != nil {
		return nil, evidence, false, generationAuthorityError("open firewall intent journal", err, nil)
	}
	defer func() {
		returnErr = appendGenerationCleanupError(
			returnErr,
			"close firewall intent journal",
			state.deps.close(context.WithoutCancel(ctx), fd),
		)
	}()
	before, err := inspectIntentFile(ctx, state, fd)
	if err != nil {
		return nil, evidence, false, err
	}
	raw, err = readStableGenerationRecord(ctx, state.deps, fd, before.size)
	if err != nil {
		return nil, evidence, false, err
	}
	after, err := inspectIntentFile(ctx, state, fd)
	if err != nil {
		return nil, evidence, false, err
	}
	if before != after {
		return nil, evidence, false, generationAuthorityError("firewall intent journal changed while reading", nil, nil)
	}
	return raw, after, true, nil
}

func (store *generationRecordStore) readIntent(
	ctx context.Context,
) (intentJournal, []byte, generationRecordFileEvidence, bool, error) {
	state := store.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if !state.active {
		return intentJournal{}, nil, generationRecordFileEvidence{}, false, generationAuthorityError(
			"firewall intent store is released",
			nil,
			nil,
		)
	}
	raw, evidence, found, err := readIntentFile(ctx, state)
	if err != nil || !found {
		return intentJournal{}, raw, evidence, found, err
	}
	journal, err := decodeIntentJournal(raw)
	return journal, raw, evidence, found, err
}

func (store *generationRecordStore) writeIntent(
	ctx context.Context,
	expected []byte,
	expectedEvidence generationRecordFileEvidence,
	journal intentJournal,
) (raw []byte, evidence generationRecordFileEvidence, returnErr error) {
	state := store.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if !state.active {
		return nil, evidence, generationAuthorityError("firewall intent store is released", nil, nil)
	}
	raw, err := json.Marshal(journal)
	if err != nil || len(raw) > maxIntentRecordBytes {
		return nil, evidence, generationAuthorityError("encode bounded firewall intent journal", err, nil)
	}
	if _, err := decodeIntentJournal(raw); err != nil {
		return nil, evidence, err
	}
	tempName, fd, err := createGenerationRecordTemp(ctx, state.deps, state.directory)
	if err != nil {
		return nil, evidence, err
	}
	open := true
	published := false
	defer func() {
		cleanupCtx := context.WithoutCancel(ctx)
		if open {
			returnErr = appendGenerationCleanupError(
				returnErr,
				"close incomplete firewall intent",
				state.deps.close(cleanupCtx, fd),
			)
		}
		if !published {
			returnErr = appendGenerationCleanupError(
				returnErr,
				"remove incomplete firewall intent",
				state.deps.unlinkAt(cleanupCtx, state.directory, tempName, 0),
			)
		}
	}()
	if err := writeAllGenerationRecord(ctx, state.deps, fd, raw); err != nil {
		return nil, evidence, err
	}
	if err := state.deps.fchown(ctx, fd, int(state.policy.expectedUID), int(state.policy.expectedGID)); err != nil {
		return nil, evidence, err
	}
	if err := state.deps.fchmod(ctx, fd, generationRecordFileMode); err != nil {
		return nil, evidence, err
	}
	before, err := inspectIntentFile(ctx, state, fd)
	if err != nil {
		return nil, evidence, err
	}
	if before.size != int64(len(raw)) {
		return nil, evidence, generationAuthorityError("firewall intent write is incomplete", nil, nil)
	}
	if err := state.deps.fsync(ctx, fd); err != nil {
		return nil, evidence, err
	}
	after, err := inspectIntentFile(ctx, state, fd)
	if err != nil {
		return nil, evidence, err
	}
	if before != after {
		return nil, evidence, generationAuthorityError("firewall intent changed before publish", nil, nil)
	}
	open = false
	if err := state.deps.close(context.WithoutCancel(ctx), fd); err != nil {
		return nil, evidence, err
	}
	current, currentEvidence, found, err := readIntentFile(ctx, state)
	if err != nil {
		return nil, evidence, err
	}
	if (expected == nil && found) ||
		(expected != nil && (!found || !bytes.Equal(current, expected) || currentEvidence != expectedEvidence)) {
		return nil, evidence, generationAuthorityError("firewall intent changed before publication", nil, nil)
	}
	if err := ctx.Err(); err != nil {
		return nil, evidence, err
	}
	if expected == nil {
		err = state.deps.renameNoReplace(ctx, state.directory, tempName, state.directory, intentRecordName)
	} else {
		err = state.deps.renameReplace(ctx, state.directory, tempName, state.directory, intentRecordName)
	}
	if err != nil {
		return nil, evidence, generationAuthorityError("publish firewall intent journal", err, nil)
	}
	published = true
	cleanupCtx := context.WithoutCancel(ctx)
	if err := state.deps.fsync(cleanupCtx, state.directory); err != nil {
		return nil, evidence, annotateGenerationRecordReplacement(
			generationAuthorityError("sync firewall intent journal directory", err, nil),
			true,
		)
	}
	readback, proof, found, err := readIntentFile(cleanupCtx, state)
	if err != nil || !found || !bytes.Equal(raw, readback) {
		return nil, evidence, generationAuthorityError("firewall intent publication outcome is unknown", err, nil)
	}
	return raw, proof, nil
}
