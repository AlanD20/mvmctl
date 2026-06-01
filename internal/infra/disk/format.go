package disk

import (
	"archive/tar"
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"os"
)

// DetectImageFormat detects container format from magic bytes. Returns "" if unknown.
func DetectImageFormat(path string) string {
	info, err := os.Stat(path)
	if err != nil || info.Size() == 0 {
		return ""
	}
	fileSize := info.Size()

	if IsQCOW2(path) {
		return "qcow2"
	}
	if IsVHD(path, fileSize) {
		return "vhd"
	}
	if IsVHDX(path) {
		return "vhdx"
	}
	if IsSquashFS(path) {
		return "squashfs"
	}
	if IsTar(path) {
		return "tar-rootfs"
	}
	if IsRaw(path, fileSize) {
		return "raw"
	}
	return ""
}

// IsQCOW2 checks if the file at path is a qcow2 image via magic bytes.
func IsQCOW2(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	buf := make([]byte, 4)
	if _, err := io.ReadFull(f, buf); err != nil {
		return false
	}
	return bytes.Equal(buf, []byte("QFI\xfb"))
}

// IsVHD checks if the file at path is a VHD image via footer cookie.
func IsVHD(path string, fileSize int64) bool {
	if fileSize < 512 {
		return false
	}
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	f.Seek(fileSize-512, io.SeekStart)
	buf := make([]byte, 8)
	if _, err := io.ReadFull(f, buf); err != nil {
		return false
	}
	return bytes.Equal(buf, []byte("conectix"))
}

// IsVHDX checks if the file at path is a VHDX image via magic bytes.
func IsVHDX(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	buf := make([]byte, 8)
	if _, err := io.ReadFull(f, buf); err != nil {
		return false
	}
	return bytes.Equal(buf, []byte("vhdxfile"))
}

// IsSquashFS checks if the file at path is a squashfs image via magic bytes.
func IsSquashFS(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	var magic uint32
	if err := binary.Read(f, binary.LittleEndian, &magic); err != nil {
		return false
	}
	return magic == 0x73717368
}

// IsTar checks if the file at path is a tar archive.
func IsTar(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	tr := tar.NewReader(f)
	_, err = tr.Next()
	return err == nil
}

// IsRaw checks if the file at path is a raw disk image.
// Returns false for files that are all-zeros or too small.
func IsRaw(path string, fileSize int64) bool {
	if fileSize < SectorSizeBytes || fileSize%SectorSizeBytes != 0 {
		return false
	}
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	firstKB := make([]byte, 1024)
	if _, err := io.ReadFull(f, firstKB); err != nil {
		return false
	}
	allZeros := true
	for _, b := range firstKB {
		if b != 0 {
			allZeros = false
			break
		}
	}
	if allZeros {
		return false
	}
	if len(firstKB) > 512 && bytes.Equal(firstKB[510:512], []byte{0x55, 0xaa}) {
		return true
	}
	if len(firstKB) > 520 && bytes.Equal(firstKB[512:520], []byte("EFI PART")) {
		return true
	}
	return true
}

// ValidateQCOW2 validates a qcow2 image, checking magic, version, and virtual size.
func ValidateQCOW2(path string) error {
	if !IsQCOW2(path) {
		return fmt.Errorf("invalid qcow2 file: wrong magic number")
	}
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open qcow2: %w", err)
	}
	defer f.Close()

	f.Read(make([]byte, 4))
	var version uint32
	if err := binary.Read(f, binary.BigEndian, &version); err != nil {
		return fmt.Errorf("read qcow2 version: %w", err)
	}
	if version != 2 && version != 3 {
		return fmt.Errorf("unsupported qcow2 version: %d (expected 2 or 3)", version)
	}

	f.Seek(24, io.SeekStart)
	var virtualSize uint64
	if err := binary.Read(f, binary.BigEndian, &virtualSize); err != nil {
		return fmt.Errorf("read qcow2 virtual size: %w", err)
	}
	if virtualSize == 0 {
		return fmt.Errorf("invalid qcow2 file: zero virtual size")
	}
	return nil
}

// ValidateVHD validates a VHD image, checking footer cookie, features, and disk type.
func ValidateVHD(path string, fileSize int64) error {
	if fileSize < 512 {
		return fmt.Errorf("invalid VHD file: too small")
	}
	if !IsVHD(path, fileSize) {
		return fmt.Errorf("invalid VHD file: missing conectix cookie")
	}
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open VHD: %w", err)
	}
	defer f.Close()

	f.Seek(fileSize-512, io.SeekStart)
	footer := make([]byte, 512)
	if _, err := io.ReadFull(f, footer); err != nil {
		return fmt.Errorf("read VHD footer: %w", err)
	}

	features := binary.BigEndian.Uint32(footer[8:12])
	if features&0x00000002 == 0 {
		return fmt.Errorf("invalid VHD file: reserved bit not set")
	}

	diskType := binary.BigEndian.Uint32(footer[60:64])
	if diskType != 2 && diskType != 3 && diskType != 4 {
		return fmt.Errorf("invalid VHD file: unknown disk type %d", diskType)
	}
	return nil
}

// ValidateVHDX validates a VHDX image, checking signature and minimum size.
func ValidateVHDX(path string, fileSize int64) error {
	if fileSize < 65536 {
		return fmt.Errorf("invalid VHDX file: too small")
	}
	if !IsVHDX(path) {
		return fmt.Errorf("invalid VHDX file: missing vhdxfile signature")
	}
	return nil
}

// ValidateRaw validates a raw disk image, checking size and non-zero header.
func ValidateRaw(path string, fileSize int64) error {
	if fileSize < SectorSizeBytes {
		return fmt.Errorf("invalid raw image: too small")
	}
	if !IsRaw(path, fileSize) {
		return fmt.Errorf("invalid raw image: file appears to be all zeros")
	}
	return nil
}

// ValidateSquashFS validates a squashfs image, checking magic and version.
func ValidateSquashFS(path string) error {
	if !IsSquashFS(path) {
		return fmt.Errorf("invalid squashfs file: wrong magic number")
	}
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open squashfs: %w", err)
	}
	defer f.Close()

	f.Seek(28, io.SeekStart)
	var major uint16
	if err := binary.Read(f, binary.LittleEndian, &major); err != nil {
		return fmt.Errorf("read squashfs version: %w", err)
	}
	var minor uint16
	if err := binary.Read(f, binary.LittleEndian, &minor); err != nil {
		return fmt.Errorf("read squashfs version: %w", err)
	}
	if major != 4 {
		return fmt.Errorf("unsupported squashfs version: %d.%d (expected 4.x)", major, minor)
	}
	return nil
}

// ValidateTar validates a tar archive by iterating through all entries.
func ValidateTar(path string) error {
	if !IsTar(path) {
		return fmt.Errorf("invalid tar file")
	}
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open tar: %w", err)
	}
	defer f.Close()
	tr := tar.NewReader(f)
	for {
		_, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read tar entry: %w", err)
		}
	}
	return nil
}
