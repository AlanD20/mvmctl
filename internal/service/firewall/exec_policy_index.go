package firewall

import (
	"bytes"
	"encoding/binary"
	"io"
	"sort"
)

const (
	execPolicyIndexMagic      = "MVMEXEC1"
	execPolicyIndexHeaderSize = 32
	execPolicyIndexVMIDSize   = 32
	execPolicyIndexUserSize   = 32
	execPolicyIndexKeySize    = execPolicyIndexVMIDSize + execPolicyIndexUserSize
	execPolicyIndexRowSize    = execPolicyIndexKeySize + 4
	execPolicyIndexCommitted  = 1
)

type execPolicyIndexHeader struct {
	status  uint32
	vmCount uint32
	rows    uint32
	bitmaps uint32
	width   uint32
}

func (projection execPolicyProjection) encodeIndex(ownerUID uint32, committed bool) []byte {
	header := execPolicyIndexHeader{}
	if committed {
		header = execPolicyIndexHeader{
			status: execPolicyIndexCommitted, vmCount: uint32(len(projection.vmIDs)),
			rows: uint32(len(projection.rows)), bitmaps: uint32(len(projection.bitmaps)),
			width: uint32((len(projection.vmIDs) + 7) / 8),
		}
	}
	data := append(make([]byte, 0, header.size()), execPolicyIndexMagic...)
	for _, value := range []uint32{ownerUID, header.status, header.vmCount, header.rows, header.bitmaps, header.width} {
		data = binary.BigEndian.AppendUint32(data, value)
	}
	if !committed {
		return data
	}
	for _, vmID := range projection.vmIDs {
		data = append(data, vmID...)
	}
	for _, row := range projection.rows {
		var key [execPolicyIndexKeySize]byte
		copy(key[:execPolicyIndexVMIDSize], row.key.sourceVMID)
		copy(key[execPolicyIndexVMIDSize:], row.key.targetUser)
		data = append(data, key[:]...)
		data = binary.BigEndian.AppendUint32(data, uint32(row.bitmap))
	}
	for _, bitmap := range projection.bitmaps {
		data = append(data, bitmap...)
	}
	return data
}

func (header execPolicyIndexHeader) size() int64 {
	return header.bitmapBase() + int64(header.bitmaps)*int64(header.width)
}

func (header execPolicyIndexHeader) rowBase() int64 {
	return execPolicyIndexHeaderSize + int64(header.vmCount)*execPolicyIndexVMIDSize
}

func (header execPolicyIndexHeader) bitmapBase() int64 {
	return header.rowBase() + int64(header.rows)*execPolicyIndexRowSize
}

func readExecPolicyIndexHeader(file io.ReaderAt, size int64, ownerUID uint32) (execPolicyIndexHeader, error) {
	var data [execPolicyIndexHeaderSize]byte
	if err := readExecIndexAt(file, data[:], 0); err != nil {
		return execPolicyIndexHeader{}, err
	}
	if string(data[:8]) != execPolicyIndexMagic || binary.BigEndian.Uint32(data[8:12]) != ownerUID {
		return execPolicyIndexHeader{}, invalidGeneration("exec authorization index identity differs")
	}
	header := execPolicyIndexHeader{
		status:  binary.BigEndian.Uint32(data[12:16]),
		vmCount: binary.BigEndian.Uint32(data[16:20]),
		rows:    binary.BigEndian.Uint32(data[20:24]),
		bitmaps: binary.BigEndian.Uint32(data[24:28]),
		width:   binary.BigEndian.Uint32(data[28:32]),
	}
	if header.status == 0 && header.vmCount == 0 && header.rows == 0 && header.bitmaps == 0 &&
		header.width == 0 && size == execPolicyIndexHeaderSize {
		return header, nil
	}
	if header.status != execPolicyIndexCommitted || header.bitmaps > header.rows ||
		(header.rows != 0 && header.bitmaps == 0) ||
		uint64(header.width) != (uint64(header.vmCount)+7)/8 || size < execPolicyIndexHeaderSize ||
		size > execPolicyProjectionBudget || header.size() != size {
		return execPolicyIndexHeader{}, invalidGeneration("exec authorization index bounds differ")
	}
	return header, nil
}

// lookupExecPolicyIndex reads an exact source/user row and one destination bit.
// It searches fixed-size sorted records without parsing the policy inventory.
func lookupExecPolicyIndex(
	file io.ReaderAt, size int64, ownerUID uint32, sourceVMID, destinationVMID, targetUser string,
) (bool, error) {
	if validateVMLabelIdentity(ownerUID, sourceVMID) != nil ||
		validateVMLabelIdentity(ownerUID, destinationVMID) != nil ||
		!validExecPolicyTargetUser(targetUser) {
		return false, invalidGeneration("invalid exec authorization identity")
	}
	header, err := readExecPolicyIndexHeader(file, size, ownerUID)
	if err != nil {
		return false, err
	}
	if header.status != execPolicyIndexCommitted || header.rows == 0 || sourceVMID == destinationVMID {
		return false, nil
	}
	var key [execPolicyIndexKeySize]byte
	copy(key[:execPolicyIndexVMIDSize], sourceVMID)
	copy(key[execPolicyIndexVMIDSize:], targetUser)
	var row [execPolicyIndexRowSize]byte
	found, err := searchExecPolicyIndex(file, header.rowBase(), int(header.rows), row[:], key[:])
	if err != nil || found < 0 {
		return false, err
	}
	bitmap := binary.BigEndian.Uint32(row[execPolicyIndexKeySize:])
	if bitmap >= header.bitmaps {
		return false, invalidGeneration("exec authorization bitmap reference is outside the index")
	}
	var vmID [execPolicyIndexVMIDSize]byte
	slot, err := searchExecPolicyIndex(
		file, execPolicyIndexHeaderSize, int(header.vmCount), vmID[:], []byte(destinationVMID),
	)
	if err != nil || slot < 0 {
		return false, err
	}
	var value [1]byte
	offset := header.bitmapBase() + int64(bitmap)*int64(header.width) + int64(slot/8)
	if err := readExecIndexAt(file, value[:], offset); err != nil {
		return false, err
	}
	return value[0]&(1<<(slot%8)) != 0, nil
}

func searchExecPolicyIndex(file io.ReaderAt, base int64, count int, record, key []byte) (int, error) {
	var readErr error
	index := sort.Search(count, func(index int) bool {
		if readErr != nil {
			return true
		}
		readErr = readExecIndexAt(file, record, base+int64(index)*int64(len(record)))
		return readErr != nil || bytes.Compare(record[:len(key)], key) >= 0
	})
	if readErr != nil || index == count {
		return -1, readErr
	}
	if err := readExecIndexAt(file, record, base+int64(index)*int64(len(record))); err != nil {
		return -1, err
	}
	if !bytes.Equal(record[:len(key)], key) {
		return -1, nil
	}
	return index, nil
}

func readExecIndexAt(file io.ReaderAt, data []byte, offset int64) error {
	n, err := file.ReadAt(data, offset)
	if err != nil || n != len(data) {
		return invalidGeneration("exec authorization index is incomplete")
	}
	return nil
}
