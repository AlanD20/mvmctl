package jailer

import (
	"fmt"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/pkg/errs"
)

func TestExecutionIdentityAccountNamePreservesFullVMIdentity(t *testing.T) {
	t.Parallel()

	// RFC 4648 base32 encodes 128 bits as 25 complete symbols and one padded
	// three-bit symbol. The suffix distinguishes IDs that differ only at the end.
	tests := map[string]struct {
		vmID string
		want string
	}{
		"zero_id": {
			vmID: strings.Repeat("0", 32),
			want: "mvm-" + strings.Repeat("a", 26),
		},
		"last_bit_changes_name": {
			vmID: strings.Repeat("0", 31) + "1",
			want: "mvm-" + strings.Repeat("a", 25) + "e",
		},
		"maximum_id": {
			vmID: strings.Repeat("f", 32),
			want: "mvm-" + strings.Repeat("7", 25) + "4",
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got, err := executionIdentityAccountName(tc.vmID)
			require.NoError(t, err)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("account name mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestExecutionIdentityAccountNameRejectsNoncanonicalVMID(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"uppercase": strings.Repeat("A", 32),
		"short":     strings.Repeat("0", 31),
		"nonhex":    strings.Repeat("0", 31) + "g",
		"path":      "../" + strings.Repeat("0", 29),
	}
	for name, vmID := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := executionIdentityAccountName(vmID)
			require.Error(t, err)
			domainErr := errs.AsDomainError(err)
			require.NotNil(t, domainErr)
			assert.Equal(t, errs.CodeValidationFailed, domainErr.Code)
		})
	}
}

func TestDecodeExecutionIdentityRecordEnforcesReservationContract(t *testing.T) {
	t.Parallel()

	vmID := strings.Repeat("0", 32)
	accountName := "mvm-" + strings.Repeat("a", 26)
	nonce := strings.Repeat("0", 32)
	pendingJSON := fmt.Sprintf(
		`{"schema":1,"owner_uid":1000,"vm_id":"%s","account_name":"%s","nonce":"%s","phase":"pending"}`,
		vmID, accountName, nonce,
	)
	readyJSON := strings.Replace(pendingJSON, `"phase":"pending"`,
		`"phase":"ready","execution_uid":2000,"execution_gid":2001`, 1)
	readyUID, readyGID := uint32(2000), uint32(2001)

	t.Run("pending_intent_has_no_execution_credentials", func(t *testing.T) {
		t.Parallel()

		got, err := decodeExecutionIdentityRecord([]byte(pendingJSON))
		require.NoError(t, err)
		want := executionIdentityRecord{
			Schema: 1, OwnerUID: 1000, VMID: vmID, Name: accountName,
			Nonce: nonce, Phase: executionIdentityPending,
		}
		if diff := cmp.Diff(want, got); diff != "" {
			t.Errorf("pending reservation mismatch (-want +got):\n%s", diff)
		}
	})

	t.Run("ready_reservation_binds_distinct_credentials", func(t *testing.T) {
		t.Parallel()

		got, err := decodeExecutionIdentityRecord([]byte(readyJSON))
		require.NoError(t, err)
		want := executionIdentityRecord{
			Schema: 1, OwnerUID: 1000, VMID: vmID, Name: accountName,
			Nonce: nonce, Phase: executionIdentityReady, UID: &readyUID, GID: &readyGID,
		}
		if diff := cmp.Diff(want, got); diff != "" {
			t.Errorf("ready reservation mismatch (-want +got):\n%s", diff)
		}
	})

	invalid := map[string]string{
		"duplicate_owner":    strings.Replace(pendingJSON, `"owner_uid":1000`, `"owner_uid":1000,"owner_uid":1000`, 1),
		"unknown_field":      strings.Replace(pendingJSON, `"phase":"pending"`, `"phase":"pending","extra":true`, 1),
		"null_field":         strings.Replace(pendingJSON, `"owner_uid":1000`, `"owner_uid":null`, 1),
		"trailing_object":    pendingJSON + `{}`,
		"wrong_account_name": strings.Replace(pendingJSON, accountName, accountName+"x", 1),
		"uppercase_nonce":    strings.Replace(pendingJSON, nonce, strings.Repeat("A", 32), 1),
		"pending_with_uid": strings.Replace(
			pendingJSON, `"phase":"pending"`, `"phase":"pending","execution_uid":2000`, 1,
		),
		"ready_without_gid":    strings.Replace(readyJSON, `,"execution_gid":2001`, "", 1),
		"operator_as_executor": strings.Replace(readyJSON, `"execution_uid":2000`, `"execution_uid":1000`, 1),
		"unknown_phase":        strings.Replace(pendingJSON, `"phase":"pending"`, `"phase":"unknown"`, 1),
		"oversized_record":     strings.Repeat(" ", executionIdentityRecordLimit+1),
	}
	for name, raw := range invalid {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := decodeExecutionIdentityRecord([]byte(raw))
			require.Error(t, err)
			domainErr := errs.AsDomainError(err)
			require.NotNil(t, domainErr)
			assert.Equal(t, errs.CodeVMAtomicFailed, domainErr.Code)
		})
	}
}
