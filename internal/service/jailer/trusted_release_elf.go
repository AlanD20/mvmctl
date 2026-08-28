package jailer

import (
	"debug/elf"
	"encoding/binary"

	"mvmctl/pkg/errs"
)

const (
	trustedReleaseELFHeaderBytes       = 64
	trustedReleaseELFProgramHeaderSize = 56
	trustedReleaseELFMaxProgramHeaders = 64
	trustedReleaseExecutableMinBytes   = uint64(
		trustedReleaseELFHeaderBytes + trustedReleaseELFProgramHeaderSize,
	)
	trustedReleaseExecutableMaxBytes = uint64(64 * 1024 * 1024)
)

// CRITICAL: This parser admits only the bounded header shape reviewed in ADR-0016. It never lets untrusted ELF counts
// drive allocation and never loads or executes the candidate bytes.
func validateTrustedReleaseELFHeader(
	raw []byte,
	sizeBytes uint64,
	source trustedReleaseSource,
) error {
	if err := validateTrustedReleaseSource(source); err != nil {
		return err
	}
	if len(raw) != trustedReleaseELFHeaderBytes {
		return trustedReleaseELFError("trusted release ELF header has invalid length")
	}
	if sizeBytes < trustedReleaseExecutableMinBytes || sizeBytes > trustedReleaseExecutableMaxBytes {
		return trustedReleaseELFError("trusted release executable size is outside the admitted range")
	}
	if raw[0] != 0x7f || raw[1] != 'E' || raw[2] != 'L' || raw[3] != 'F' {
		return trustedReleaseELFError("trusted release executable has invalid ELF magic")
	}
	if elf.Class(raw[4]) != elf.ELFCLASS64 {
		return trustedReleaseELFError("trusted release executable is not ELF64")
	}
	if elf.Data(raw[5]) != elf.ELFDATA2LSB {
		return trustedReleaseELFError("trusted release executable is not little-endian ELF")
	}
	if elf.Version(raw[6]) != elf.EV_CURRENT || elf.OSABI(raw[7]) != elf.ELFOSABI_NONE || raw[8] != 0 {
		return trustedReleaseELFError("trusted release executable has unsupported ELF identity")
	}
	for _, value := range raw[9:16] {
		if value != 0 {
			return trustedReleaseELFError("trusted release executable has non-zero ELF identity padding")
		}
	}
	if elf.Type(binary.LittleEndian.Uint16(raw[16:18])) != elf.ET_DYN {
		return trustedReleaseELFError("trusted release executable is not a position-independent ELF object")
	}
	machine := elf.Machine(binary.LittleEndian.Uint16(raw[18:20]))
	expectedMachine := elf.EM_X86_64
	if source.slot.architecture == "aarch64" {
		expectedMachine = elf.EM_AARCH64
	}
	if machine != expectedMachine {
		return trustedReleaseELFError("trusted release executable architecture does not match release slot")
	}
	if elf.Version(binary.LittleEndian.Uint32(raw[20:24])) != elf.EV_CURRENT {
		return trustedReleaseELFError("trusted release executable has unsupported ELF object version")
	}
	if binary.LittleEndian.Uint64(raw[24:32]) == 0 {
		return trustedReleaseELFError("trusted release executable has no ELF entry point")
	}
	programHeaderOffset := binary.LittleEndian.Uint64(raw[32:40])
	if programHeaderOffset != trustedReleaseELFHeaderBytes {
		return trustedReleaseELFError("trusted release executable has unexpected program-header offset")
	}
	if binary.LittleEndian.Uint16(raw[52:54]) != trustedReleaseELFHeaderBytes {
		return trustedReleaseELFError("trusted release executable has unexpected ELF header size")
	}
	if binary.LittleEndian.Uint16(raw[54:56]) != trustedReleaseELFProgramHeaderSize {
		return trustedReleaseELFError("trusted release executable has unexpected program-header size")
	}
	programHeaderCount := binary.LittleEndian.Uint16(raw[56:58])
	if programHeaderCount == 0 || programHeaderCount > trustedReleaseELFMaxProgramHeaders {
		return trustedReleaseELFError("trusted release executable has unsupported program-header count")
	}
	programHeaderEnd := programHeaderOffset +
		uint64(trustedReleaseELFProgramHeaderSize)*uint64(programHeaderCount)
	if sizeBytes < programHeaderEnd {
		return trustedReleaseELFError("trusted release executable has a truncated program-header table")
	}
	return nil
}

func trustedReleaseELFError(message string) *errs.DomainError {
	return errs.New(errs.CodeBinaryUntrusted, message)
}
