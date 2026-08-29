package jailer

import (
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"

	"mvmctl/pkg/errs"
)

const (
	trustedReleaseArchivePAXHeaderName = "././@PaxHeader"
	trustedReleaseArchiveReadBytes     = 32 * 1024
)

type trustedReleaseArchiveSelectedWriter interface {
	writeFirecracker(context.Context, []byte, uint64) error
	writeJailer(context.Context, []byte, uint64) error
}

type trustedReleaseArchivePositionedReader struct {
	ctx    context.Context
	stage  *trustedReleaseArchiveStage
	offset uint64
}

type trustedReleaseArchiveBoundedReader struct {
	ctx      context.Context
	reader   io.Reader
	consumed uint64
}

// CRITICAL: Extraction accepts only a ready checksum-matched anonymous stage. It consumes the closed gzip/PAX/tar
// contract exactly once, writes only the two named selections, and leaves the archive descriptor offset unchanged.
// The selected writer is a private staging boundary; this method publishes no pathname.
func (stage *trustedReleaseArchiveStage) extract(
	ctx context.Context,
	policy trustedReleaseArchivePolicy,
	selected trustedReleaseArchiveSelectedWriter,
) error {
	if stage == nil || stage.fd < 0 {
		return trustedReleaseStoreError("trusted release archive stage is not active", nil)
	}
	if stage.state != trustedReleaseArchiveStageReady {
		return trustedReleaseStoreError("trusted release archive stage is not ready for extraction", nil)
	}
	if selected == nil {
		return trustedReleaseStoreError("trusted release archive selected writer is not available", nil)
	}
	if err := validateTrustedReleaseArchivePolicy(policy); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return trustedReleaseStoreError("extract trusted release archive", err)
	}

	initial, err := inspectTrustedReleaseArchiveStage(
		ctx,
		stage.deps,
		stage.policy,
		stage.fd,
		stage.sizeBytes,
		true,
	)
	if err != nil {
		return err
	}
	if err := requireTrustedReleaseArchiveStageZeroOffset(ctx, stage.deps, stage.fd); err != nil {
		return err
	}
	stage.state = trustedReleaseArchiveStageFailed

	compressed := &trustedReleaseArchivePositionedReader{ctx: ctx, stage: stage}
	gzipReader, err := gzip.NewReader(compressed)
	if err != nil {
		return classifyTrustedReleaseArchiveContentError("trusted release archive gzip header is invalid", err)
	}
	gzipReader.Multistream(false)
	decompressed := &trustedReleaseArchiveBoundedReader{ctx: ctx, reader: gzipReader}
	parseErr := consumeTrustedReleaseArchive(ctx, decompressed, policy, selected)
	closeErr := gzipReader.Close()
	if parseErr != nil {
		return appendTrustedReleaseStoreError(parseErr, "close rejected trusted release gzip reader", closeErr)
	}
	if closeErr != nil {
		return classifyTrustedReleaseArchiveContentError("close trusted release gzip reader", closeErr)
	}
	if compressed.offset != stage.sizeBytes {
		return trustedReleaseArchiveFormatError(
			"trusted release archive contains a concatenated gzip member or compressed trailing bytes",
		)
	}

	final, err := inspectTrustedReleaseArchiveStage(
		ctx,
		stage.deps,
		stage.policy,
		stage.fd,
		stage.sizeBytes,
		true,
	)
	if err != nil {
		return err
	}
	if !sameTrustedReleaseArchiveStageIdentity(initial, final) {
		return trustedReleaseStoreError("trusted release archive stage identity changed while extracting", nil)
	}
	if err := requireTrustedReleaseArchiveStageZeroOffset(ctx, stage.deps, stage.fd); err != nil {
		return err
	}
	stage.state = trustedReleaseArchiveStageExtracted
	return nil
}

func validateTrustedReleaseArchivePolicy(policy trustedReleaseArchivePolicy) error {
	expected, err := newTrustedReleaseArchivePolicy(policy.source)
	if err != nil {
		return err
	}
	if len(policy.members) != len(expected.members) {
		return trustedReleaseArchiveFormatError("trusted release archive policy identity is inconsistent")
	}
	for name, member := range expected.members {
		if policy.members[name] != member {
			return trustedReleaseArchiveFormatError("trusted release archive policy identity is inconsistent")
		}
	}
	return nil
}

func consumeTrustedReleaseArchive(
	ctx context.Context,
	reader io.Reader,
	policy trustedReleaseArchivePolicy,
	selected trustedReleaseArchiveSelectedWriter,
) error {
	remaining := make(map[string]trustedReleaseArchiveMemberPolicy, len(policy.members))
	for name, member := range policy.members {
		remaining[name] = member
	}
	memberBuffer := make([]byte, trustedReleaseArchiveReadBytes)
	var block [trustedReleaseTarBlockBytes]byte
	memberCount := 0
	for {
		if err := readTrustedReleaseArchiveExact(ctx, reader, block[:], "read trusted release PAX header"); err != nil {
			return err
		}
		if allTrustedReleaseArchiveBytesZero(block[:]) {
			if memberCount != trustedReleaseArchiveMemberCount || len(remaining) != 0 {
				return trustedReleaseArchiveFormatError("trusted release archive is missing required members")
			}
			if err := readTrustedReleaseArchiveExact(
				ctx,
				reader,
				block[:],
				"read trusted release second tar end block",
			); err != nil {
				return err
			}
			if !allTrustedReleaseArchiveBytesZero(block[:]) {
				return trustedReleaseArchiveFormatError("trusted release archive has an invalid second tar end block")
			}
			return drainTrustedReleaseArchiveZeroTail(ctx, reader, memberBuffer)
		}
		if memberCount >= trustedReleaseArchiveMemberCount {
			return trustedReleaseArchiveFormatError("trusted release archive contains too many members")
		}

		paxHeader, err := parseTrustedReleaseTarHeader(block[:])
		if err != nil {
			return err
		}
		if err := validateTrustedReleaseArchivePAXHeader(paxHeader); err != nil {
			return err
		}
		paxPayload := make([]byte, paxHeader.sizeBytes)
		if err := readTrustedReleaseArchiveExact(
			ctx,
			reader,
			paxPayload,
			"read trusted release PAX payload",
		); err != nil {
			return err
		}
		if err := readTrustedReleaseArchivePadding(ctx, reader, paxHeader.sizeBytes); err != nil {
			return err
		}
		if err := validateTrustedReleasePAXRecords(paxPayload); err != nil {
			return err
		}

		if err := readTrustedReleaseArchiveExact(
			ctx,
			reader,
			block[:],
			"read trusted release member header",
		); err != nil {
			return err
		}
		memberHeader, err := parseTrustedReleaseTarHeader(block[:])
		if err != nil {
			return err
		}
		memberPolicy, present := remaining[memberHeader.name]
		if !present {
			return trustedReleaseArchiveFormatError(
				"trusted release archive contains an unexpected or duplicate member",
			)
		}
		if err := validateTrustedReleaseArchiveMemberHeader(memberHeader, memberPolicy); err != nil {
			return err
		}
		if err := consumeTrustedReleaseArchiveMember(
			ctx,
			reader,
			memberBuffer,
			memberHeader,
			memberPolicy,
			selected,
		); err != nil {
			return err
		}
		if err := readTrustedReleaseArchivePadding(ctx, reader, memberHeader.sizeBytes); err != nil {
			return err
		}
		delete(remaining, memberHeader.name)
		memberCount++
	}
}

func validateTrustedReleaseArchivePAXHeader(header trustedReleaseTarHeader) error {
	if header.name != trustedReleaseArchivePAXHeaderName || header.typeFlag != trustedReleaseTarTypePAX ||
		header.mode != 0 || header.uid != 0 || header.gid != 0 || header.modTime != 0 ||
		header.linkName != "" || header.userName != "" || header.groupName != "" ||
		header.deviceMajor != 0 || header.deviceMinor != 0 ||
		header.sizeBytes == 0 || header.sizeBytes > trustedReleaseArchiveMaxPAXBytes {
		return trustedReleaseArchiveFormatError("trusted release archive PAX header is outside the audited contract")
	}
	return nil
}

func validateTrustedReleaseArchiveMemberHeader(
	header trustedReleaseTarHeader,
	policy trustedReleaseArchiveMemberPolicy,
) error {
	if header.typeFlag != trustedReleaseTarTypeRegular || header.linkName != "" ||
		header.deviceMajor != 0 || header.deviceMinor != 0 {
		return trustedReleaseArchiveFormatError("trusted release archive member is not an admitted regular file")
	}
	if header.mode != policy.mode {
		return trustedReleaseArchiveFormatError("trusted release archive member mode does not match policy")
	}
	if header.sizeBytes > trustedReleaseArchiveMaxMemberBytes {
		return trustedReleaseArchiveFormatError("trusted release archive member size exceeds policy")
	}
	return nil
}

func consumeTrustedReleaseArchiveMember(
	ctx context.Context,
	reader io.Reader,
	buffer []byte,
	header trustedReleaseTarHeader,
	policy trustedReleaseArchiveMemberPolicy,
	selected trustedReleaseArchiveSelectedWriter,
) error {
	var offset uint64
	for offset < header.sizeBytes {
		readBytes := len(buffer)
		if remaining := header.sizeBytes - offset; remaining < uint64(readBytes) {
			readBytes = int(remaining)
		}
		chunk := buffer[:readBytes]
		if err := readTrustedReleaseArchiveExact(ctx, reader, chunk, "read trusted release member bytes"); err != nil {
			return err
		}
		var err error
		switch policy.selected {
		case trustedReleaseArchiveNotSelected:
		case trustedReleaseArchiveFirecracker:
			err = selected.writeFirecracker(ctx, chunk, offset)
		case trustedReleaseArchiveJailer:
			err = selected.writeJailer(ctx, chunk, offset)
		default:
			return trustedReleaseStoreError("trusted release archive selection policy is invalid", nil)
		}
		if err != nil {
			if errs.AsDomainError(err) != nil {
				return err
			}
			return trustedReleaseStoreError("write selected trusted release archive member", err)
		}
		offset += uint64(readBytes)
	}
	return nil
}

func readTrustedReleaseArchivePadding(ctx context.Context, reader io.Reader, sizeBytes uint64) error {
	paddingBytes := (trustedReleaseTarBlockBytes - int(sizeBytes%trustedReleaseTarBlockBytes)) %
		trustedReleaseTarBlockBytes
	if paddingBytes == 0 {
		return nil
	}
	var padding [trustedReleaseTarBlockBytes]byte
	if err := readTrustedReleaseArchiveExact(
		ctx,
		reader,
		padding[:paddingBytes],
		"read trusted release tar padding",
	); err != nil {
		return err
	}
	if !allTrustedReleaseArchiveBytesZero(padding[:paddingBytes]) {
		return trustedReleaseArchiveFormatError("trusted release archive contains non-zero tar padding")
	}
	return nil
}

func readTrustedReleaseArchiveExact(
	ctx context.Context,
	reader io.Reader,
	destination []byte,
	description string,
) error {
	if err := ctx.Err(); err != nil {
		return trustedReleaseStoreError(description, err)
	}
	_, err := io.ReadFull(reader, destination)
	if err == nil {
		return nil
	}
	return classifyTrustedReleaseArchiveContentError(description, err)
}

func drainTrustedReleaseArchiveZeroTail(ctx context.Context, reader io.Reader, buffer []byte) error {
	for {
		if err := ctx.Err(); err != nil {
			return trustedReleaseStoreError("read trusted release tar tail", err)
		}
		count, readErr := reader.Read(buffer)
		if count < 0 || count > len(buffer) {
			return trustedReleaseStoreError(
				"read trusted release tar tail",
				fmt.Errorf("invalid read count %d", count),
			)
		}
		if count > 0 && !allTrustedReleaseArchiveBytesZero(buffer[:count]) {
			return trustedReleaseArchiveFormatError("trusted release archive contains non-zero tar tail bytes")
		}
		if errors.Is(readErr, io.EOF) {
			return nil
		}
		if readErr != nil {
			return classifyTrustedReleaseArchiveContentError("read trusted release tar tail", readErr)
		}
		if count == 0 {
			return trustedReleaseStoreError("read trusted release tar tail", io.ErrNoProgress)
		}
	}
}

func (reader *trustedReleaseArchivePositionedReader) Read(destination []byte) (int, error) {
	if len(destination) == 0 {
		return 0, nil
	}
	if err := reader.ctx.Err(); err != nil {
		return 0, trustedReleaseStoreError("read trusted release archive stage", err)
	}
	if reader.offset >= reader.stage.sizeBytes {
		return 0, io.EOF
	}
	if remaining := reader.stage.sizeBytes - reader.offset; remaining < uint64(len(destination)) {
		destination = destination[:remaining]
	}
	count, readErr := reader.stage.deps.pread(
		reader.ctx,
		reader.stage.fd,
		destination,
		int64(reader.offset),
	)
	if count < 0 || count > len(destination) {
		return 0, trustedReleaseStoreError(
			"read trusted release archive stage",
			fmt.Errorf("invalid positioned read count %d", count),
		)
	}
	reader.offset += uint64(count)
	if readErr != nil {
		return count, trustedReleaseStoreError("read trusted release archive stage", readErr)
	}
	if count == 0 {
		return 0, trustedReleaseStoreError("read trusted release archive stage", io.ErrNoProgress)
	}
	return count, nil
}

func (reader *trustedReleaseArchivePositionedReader) ReadByte() (byte, error) {
	var value [1]byte
	count, err := reader.Read(value[:])
	if count == 1 {
		return value[0], err
	}
	return 0, err
}

func (reader *trustedReleaseArchiveBoundedReader) Read(destination []byte) (int, error) {
	if len(destination) == 0 {
		return 0, nil
	}
	if err := reader.ctx.Err(); err != nil {
		return 0, trustedReleaseStoreError("decompress trusted release archive", err)
	}
	if reader.consumed == trustedReleaseArchiveMaxDecompressedBytes {
		var probe [1]byte
		count, readErr := reader.reader.Read(probe[:])
		if count < 0 || count > len(probe) {
			return 0, trustedReleaseStoreError(
				"decompress trusted release archive",
				fmt.Errorf("invalid read count %d", count),
			)
		}
		if count != 0 {
			return 0, trustedReleaseArchiveFormatError(
				"trusted release archive decompressed size exceeds policy",
			)
		}
		if readErr != nil {
			return 0, readErr
		}
		return 0, trustedReleaseStoreError("decompress trusted release archive", io.ErrNoProgress)
	}
	remaining := trustedReleaseArchiveMaxDecompressedBytes - reader.consumed
	if remaining < uint64(len(destination)) {
		destination = destination[:remaining]
	}
	count, readErr := reader.reader.Read(destination)
	if count < 0 || count > len(destination) {
		return 0, trustedReleaseStoreError(
			"decompress trusted release archive",
			fmt.Errorf("invalid read count %d", count),
		)
	}
	reader.consumed += uint64(count)
	if count == 0 && readErr == nil {
		return 0, trustedReleaseStoreError("decompress trusted release archive", io.ErrNoProgress)
	}
	return count, readErr
}

func classifyTrustedReleaseArchiveContentError(message string, cause error) error {
	if errs.AsDomainError(cause) != nil {
		return cause
	}
	return trustedReleaseStoreUntrusted(message, cause)
}

var _ io.Reader = (*trustedReleaseArchivePositionedReader)(nil)
var _ io.ByteReader = (*trustedReleaseArchivePositionedReader)(nil)
var _ io.Reader = (*trustedReleaseArchiveBoundedReader)(nil)
