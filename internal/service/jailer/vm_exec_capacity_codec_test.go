package jailer

import (
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/pkg/errs"
)

const (
	testVMExecReservationID = "11223344556677889900aabbccddeeff"
	testVMExecDestinationID = "fedcba9876543210fedcba9876543210"
)

func TestVMExecCapacityRecordCodecRoundTrip(t *testing.T) {
	t.Parallel()

	record := testVMExecCapacityRecord()
	raw, err := encodeVMExecCapacityRecord(record)
	require.NoError(t, err)
	wantJSON := `{"schema_version":1,"kind":"vm_exec","reservation_id":"11223344556677889900aabbccddeeff",` +
		`"owner_uid":1000,"host_boot_id":"123e4567-e89b-42d3-a456-426614174000",` +
		`"source_vm_id":"0123456789abcdef0123456789abcdef","source_launch_generation":7,` +
		`"destination_vm_id":"fedcba9876543210fedcba9876543210","destination_launch_generation":9,` +
		`"created_boottime_ns":1000000000,"expires_boottime_ns":301000000000}`
	assert.Equal(t, wantJSON, string(raw))

	got, err := decodeVMExecCapacityRecord(raw)
	require.NoError(t, err)
	if diff := cmp.Diff(record, got, cmp.AllowUnexported(vmExecCapacityRecord{}, vmExecReservationID{})); diff != "" {
		t.Errorf("VM-exec capacity record mismatch (-want +got):\n%s", diff)
	}
}

func TestVMExecCapacityRecordCodecRejectsNonCanonicalInput(t *testing.T) {
	t.Parallel()

	valid, err := encodeVMExecCapacityRecord(testVMExecCapacityRecord())
	require.NoError(t, err)
	validText := string(valid)
	tests := map[string]string{
		"case-folded duplicate": strings.Replace(validText, `"owner_uid":`, `"OWNER_UID":1000,"owner_uid":`, 1),
		"duplicate":             strings.Replace(validText, `"owner_uid":`, `"owner_uid":1000,"owner_uid":`, 1),
		"missing":               strings.Replace(validText, `,"owner_uid":1000`, "", 1),
		"nested scalar":         strings.Replace(validText, `"owner_uid":1000`, `"owner_uid":{"value":1000}`, 1),
		"trailing":              validText + `{}`,
		"unknown":               strings.Replace(validText, `"owner_uid":`, `"unexpected":1,"owner_uid":`, 1),
		"wrong kind":            strings.Replace(validText, `"vm_exec"`, `"other"`, 1),
	}
	for name, raw := range tests {
		t.Run(name, func(t *testing.T) {
			_, decodeErr := decodeVMExecCapacityRecord([]byte(raw))
			require.Error(t, decodeErr)
			domainErr := errs.AsDomainError(decodeErr)
			require.NotNil(t, domainErr)
			assert.Equal(t, errs.CodeVMAtomicFailed, domainErr.Code)
		})
	}
}

func TestVMExecCapacityRecordValidationRejectsInvalidAuthority(t *testing.T) {
	t.Parallel()

	tests := map[string]func(*vmExecCapacityRecord){
		"empty reservation": func(record *vmExecCapacityRecord) { record.reservationID = vmExecReservationID{} },
		"zero owner":        func(record *vmExecCapacityRecord) { record.ownerUID = 0 },
		"invalid boot":      func(record *vmExecCapacityRecord) { record.hostBootID = "invalid" },
		"same VM":           func(record *vmExecCapacityRecord) { record.destinationVMID = record.sourceVMID },
		"zero source gen":   func(record *vmExecCapacityRecord) { record.sourceLaunchGeneration = 0 },
		"zero dest gen":     func(record *vmExecCapacityRecord) { record.destinationLaunchGeneration = 0 },
		"empty lifetime": func(record *vmExecCapacityRecord) {
			record.expiresBoottimeNS = record.createdBoottimeNS
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			record := testVMExecCapacityRecord()
			mutate(&record)
			_, err := encodeVMExecCapacityRecord(record)
			require.Error(t, err)
		})
	}
}

func TestNewVMExecReservationIDRequiresExactLowerHex(t *testing.T) {
	t.Parallel()

	valid, err := newVMExecReservationID(testVMExecReservationID)
	require.NoError(t, err)
	assert.Equal(t, testVMExecReservationID, valid.value)
	for _, value := range []string{"", strings.ToUpper(testVMExecReservationID), testVMExecReservationID + "0"} {
		_, invalidErr := newVMExecReservationID(value)
		require.Error(t, invalidErr)
	}
}

func testVMExecCapacityRecord() vmExecCapacityRecord {
	reservationID, err := newVMExecReservationID(testVMExecReservationID)
	if err != nil {
		panic(err)
	}
	return vmExecCapacityRecord{
		schemaVersion:               vmExecCapacityRecordSchemaVersion,
		kind:                        vmExecCapacityKind,
		reservationID:               reservationID,
		ownerUID:                    1000,
		hostBootID:                  "123e4567-e89b-42d3-a456-426614174000",
		sourceVMID:                  testVMID,
		sourceLaunchGeneration:      7,
		destinationVMID:             testVMExecDestinationID,
		destinationLaunchGeneration: 9,
		createdBoottimeNS:           1_000_000_000,
		expiresBoottimeNS:           301_000_000_000,
	}
}
