package jailer

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"

	"golang.org/x/sys/unix"
)

const (
	trustedReleaseArchiveMaxBytes          = uint64(128 * 1024 * 1024)
	trustedReleaseArchiveStreamBufferBytes = 32 * 1024
	trustedReleaseArchiveMaxEmptyReads     = 100
)

type trustedReleaseArchiveStageState uint8

const (
	trustedReleaseArchiveStageEmpty trustedReleaseArchiveStageState = iota
	trustedReleaseArchiveStageReady
	trustedReleaseArchiveStageFailed
	trustedReleaseArchiveStageExtracted
)

// CRITICAL: Body is the authenticated raw request stream positioned at the payload start, not a pre-limited reader;
// this method owns exact EOF admission. The reader transports bytes but grants no authority. Only the independently
// obtained digest can promote this single-use anonymous stage to ready, and any started failure permanently poisons it.
func (stage *trustedReleaseArchiveStage) receive(
	ctx context.Context,
	body io.Reader,
	declaredBytes uint64,
	expectedDigest trustedReleaseArchiveDigest,
) error {
	if err := stage.requireEmptyForReceive(); err != nil {
		return err
	}
	if declaredBytes == 0 || declaredBytes > trustedReleaseArchiveMaxBytes {
		return trustedReleaseStoreUntrusted("trusted release archive stream size is outside policy", nil)
	}
	if body == nil {
		return trustedReleaseStoreError("trusted release archive stream is not available", nil)
	}
	if err := ctx.Err(); err != nil {
		return trustedReleaseStoreError("receive trusted release archive stream", err)
	}

	initial, err := inspectTrustedReleaseArchiveStage(
		ctx,
		stage.deps,
		stage.policy,
		stage.fd,
		0,
		true,
	)
	if err != nil {
		return err
	}
	if err := requireTrustedReleaseArchiveStageZeroOffset(ctx, stage.deps, stage.fd); err != nil {
		return err
	}
	stage.state = trustedReleaseArchiveStageFailed

	digest, err := stage.writeAndHashArchive(ctx, body, declaredBytes)
	if err != nil {
		return err
	}
	if digest != expectedDigest {
		return trustedReleaseStoreUntrusted("trusted release archive digest does not match authority", nil)
	}
	if err := ctx.Err(); err != nil {
		return trustedReleaseStoreError("sync trusted release archive stage", err)
	}
	if err := stage.deps.fsync(ctx, stage.fd); err != nil {
		return trustedReleaseStoreError("sync trusted release archive stage", err)
	}
	final, err := inspectTrustedReleaseArchiveStage(
		ctx,
		stage.deps,
		stage.policy,
		stage.fd,
		declaredBytes,
		true,
	)
	if err != nil {
		return err
	}
	if !sameTrustedReleaseArchiveStageIdentity(initial, final) {
		return trustedReleaseStoreError("trusted release archive stage identity changed while receiving bytes", nil)
	}
	if err := requireTrustedReleaseArchiveStageZeroOffset(ctx, stage.deps, stage.fd); err != nil {
		return err
	}

	stage.sizeBytes = declaredBytes
	stage.archiveDigest = digest
	stage.state = trustedReleaseArchiveStageReady
	return nil
}

func (stage *trustedReleaseArchiveStage) requireEmptyForReceive() error {
	if stage == nil || stage.fd < 0 {
		return trustedReleaseStoreError("trusted release archive stage is not active", nil)
	}
	if stage.state != trustedReleaseArchiveStageEmpty {
		return trustedReleaseStoreError("trusted release archive stage is not empty", nil)
	}
	return nil
}

func (stage *trustedReleaseArchiveStage) poisonReadyAfterReceive() {
	if stage == nil || stage.state != trustedReleaseArchiveStageReady {
		return
	}
	stage.state = trustedReleaseArchiveStageFailed
	stage.sizeBytes = 0
	stage.archiveDigest = trustedReleaseArchiveDigest{}
}

func (stage *trustedReleaseArchiveStage) writeAndHashArchive(
	ctx context.Context,
	body io.Reader,
	declaredBytes uint64,
) (trustedReleaseArchiveDigest, error) {
	hasher := sha256.New()
	buffer := make([]byte, trustedReleaseArchiveStreamBufferBytes)
	var offset uint64
	emptyReads := 0
	for offset < declaredBytes {
		if err := ctx.Err(); err != nil {
			return trustedReleaseArchiveDigest{}, trustedReleaseStoreError(
				"receive trusted release archive stream",
				err,
			)
		}
		remaining := declaredBytes - offset
		readSize := len(buffer)
		if remaining < uint64(readSize) {
			readSize = int(remaining)
		}
		count, readErr := body.Read(buffer[:readSize])
		if count < 0 || count > readSize {
			return trustedReleaseArchiveDigest{}, trustedReleaseStoreError(
				"receive trusted release archive stream",
				fmt.Errorf("invalid read count %d", count),
			)
		}
		if count > 0 {
			emptyReads = 0
			if err := ctx.Err(); err != nil {
				return trustedReleaseArchiveDigest{}, trustedReleaseStoreError(
					"receive trusted release archive stream",
					err,
				)
			}
			if err := stage.writeArchiveChunk(ctx, buffer[:count], offset); err != nil {
				return trustedReleaseArchiveDigest{}, err
			}
			if _, err := hasher.Write(buffer[:count]); err != nil {
				return trustedReleaseArchiveDigest{}, trustedReleaseStoreError(
					"hash trusted release archive stream",
					err,
				)
			}
			offset += uint64(count)
		} else {
			emptyReads++
		}
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return trustedReleaseArchiveDigest{}, trustedReleaseStoreError(
				"receive trusted release archive stream",
				readErr,
			)
		}
		if offset < declaredBytes && errors.Is(readErr, io.EOF) {
			return trustedReleaseArchiveDigest{}, trustedReleaseStoreUntrusted(
				"trusted release archive stream is shorter than declared",
				nil,
			)
		}
		if count == 0 && readErr == nil && emptyReads >= trustedReleaseArchiveMaxEmptyReads {
			return trustedReleaseArchiveDigest{}, trustedReleaseStoreError(
				"receive trusted release archive stream",
				io.ErrNoProgress,
			)
		}
	}

	if err := requireTrustedReleaseArchiveEOF(ctx, body); err != nil {
		return trustedReleaseArchiveDigest{}, err
	}
	var digest trustedReleaseArchiveDigest
	copy(digest[:], hasher.Sum(nil))
	return digest, nil
}

func (stage *trustedReleaseArchiveStage) writeArchiveChunk(
	ctx context.Context,
	value []byte,
	offset uint64,
) error {
	for written := 0; written < len(value); {
		if err := ctx.Err(); err != nil {
			return trustedReleaseStoreError("write trusted release archive stage", err)
		}
		count, writeErr := stage.deps.pwrite(ctx, stage.fd, value[written:], int64(offset)+int64(written))
		if count < 0 || count > len(value)-written {
			return trustedReleaseStoreError(
				"write trusted release archive stage",
				fmt.Errorf("invalid positioned write count %d", count),
			)
		}
		written += count
		if writeErr != nil {
			return trustedReleaseStoreError("write trusted release archive stage", writeErr)
		}
		if count == 0 {
			return trustedReleaseStoreError("write trusted release archive stage", io.ErrNoProgress)
		}
	}
	return nil
}

func requireTrustedReleaseArchiveEOF(ctx context.Context, body io.Reader) error {
	var probe [1]byte
	for emptyReads := 0; ; emptyReads++ {
		if err := ctx.Err(); err != nil {
			return trustedReleaseStoreError("probe trusted release archive stream length", err)
		}
		count, readErr := body.Read(probe[:])
		if count < 0 || count > len(probe) {
			return trustedReleaseStoreError(
				"probe trusted release archive stream length",
				fmt.Errorf("invalid read count %d", count),
			)
		}
		if count != 0 {
			return trustedReleaseStoreUntrusted("trusted release archive stream is longer than declared", nil)
		}
		if errors.Is(readErr, io.EOF) {
			return nil
		}
		if readErr != nil {
			return trustedReleaseStoreError("probe trusted release archive stream length", readErr)
		}
		if emptyReads+1 >= trustedReleaseArchiveMaxEmptyReads {
			return trustedReleaseStoreError("probe trusted release archive stream length", io.ErrNoProgress)
		}
	}
}

func sameTrustedReleaseArchiveStageIdentity(first, second unix.Stat_t) bool {
	return first.Dev == second.Dev &&
		first.Ino == second.Ino &&
		first.Mode == second.Mode &&
		first.Nlink == second.Nlink &&
		first.Uid == second.Uid &&
		first.Gid == second.Gid
}
