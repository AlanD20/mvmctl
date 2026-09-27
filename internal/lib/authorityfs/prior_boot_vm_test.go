package authorityfs

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// This record is written in the production schema-5 shape independently of
// Jailer's encoder, so a writer and reader cannot share the same field bug.
const priorBootSchemaFiveCleaning = `{"schema_version":5,"owner_uid":1000,` +
	`"vm_id":"0123456789abcdef0123456789abcdef","network_placement":"managed",` +
	`"lifecycle":"cleaning","launch_generation":1,"cleanup_generation":1,"launch":{` +
	`"owner_gid":1001,"network_placement":"managed","execution_identity":{"uid":2000,"gid":2001},` +
	`"snapshot_id":"","cache_locator":"/home/caller/mvmctl",` +
	`"host_boot_id":"123e4567-e89b-42d3-a456-426614174001","pid":4242,"start_time_ticks":987654,` +
	`"relay":{"kind":"absent"},"managed_cache":{"device_major":8,"device_minor":1,"inode":12345,` +
	`"mount_identity":{"kind":"unique","id":67890}},` +
	`"release":{"version":"1.16.0","architecture":"x86_64",` +
	`"firecracker_sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",` +
	`"jailer_sha256":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}}}`

func TestDecodePriorBootSchemaFiveCleaning(t *testing.T) {
	t.Parallel()
	key, err := NewVMKey("0123456789abcdef0123456789abcdef")
	require.NoError(t, err)

	withOutputs := strings.Replace(priorBootSchemaFiveCleaning,
		`"snapshot_id":""`,
		`"snapshot_id":"","output_identity":{"log":{"device":71,"inode":72},`+
			`"metrics":{"device":73,"inode":74}}`, 1)
	for name, raw := range map[string]string{
		"cleaning_from_launch":          priorBootSchemaFiveCleaning,
		"cleaning_with_output_identity": withOutputs,
	} {
		t.Run(name, func(t *testing.T) {
			got, err := decodePriorBootRecord([]byte(raw), 1000, key)
			require.NoError(t, err)
			assert.Equal(t, uint32(5), got.SchemaVersion)
			assert.Equal(t, key.value, got.VMID)
			assert.Equal(t, "managed", got.NetworkPlacement)
			assert.Equal(t, "managed", got.Launch.NetworkPlacement)
			assert.Equal(t, "cleaning", got.Lifecycle)
			assert.Equal(t, uint64(1), got.LaunchGeneration)
			assert.Equal(t, uint64(1), got.CleanupGeneration)
			assert.Equal(t, "123e4567-e89b-42d3-a456-426614174001", got.Launch.HostBootID)
			assert.Equal(t, "/home/caller/mvmctl", got.Launch.CacheLocator)
			assert.NotEmpty(t, got.Launch.ExecutionIdentity)
			if name == "cleaning_with_output_identity" {
				assert.NotEmpty(t, got.Launch.OutputIdentity)
			}
		})
	}
}

func TestDecodePriorBootSchemaFiveCleaningFromActive(t *testing.T) {
	t.Parallel()
	key, err := NewVMKey("0123456789abcdef0123456789abcdef")
	require.NoError(t, err)
	const active = `,"active":{"user_ids":{"real":2000,"effective":2000,"saved":2000,"filesystem":2000},` +
		`"group_ids":{"real":2001,"effective":2001,"saved":2001,"filesystem":2001},` +
		`"supplementary_groups":[],"mount_namespace":{"device":41,"inode":42},` +
		`"jail_root":{"device":43,"inode":44},` +
		`"executable":{"device":45,"inode":46,"size_bytes":120,"mode":33261,"uid":0,"gid":0,"links":1,` +
		`"manifest_sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},` +
		`"relay":{"kind":"absent"}}}`
	raw := strings.TrimSuffix(priorBootSchemaFiveCleaning, "}") + active
	got, err := decodePriorBootRecord([]byte(raw), 1000, key)
	require.NoError(t, err)
	assert.NotEmpty(t, got.Active)
}

func TestDecodePriorBootRecordRejectsInvalidBinding(t *testing.T) {
	t.Parallel()
	key, err := NewVMKey("0123456789abcdef0123456789abcdef")
	require.NoError(t, err)

	tests := map[string]string{
		"schema_three": strings.Replace(priorBootSchemaFiveCleaning, `"schema_version":5`, `"schema_version":3`, 1),
		"schema_four":  strings.Replace(priorBootSchemaFiveCleaning, `"schema_version":5`, `"schema_version":4`, 1),
		"unknown_record_field": strings.Replace(priorBootSchemaFiveCleaning, `"lifecycle":"cleaning"`,
			`"future":1,"lifecycle":"cleaning"`, 1),
		"unknown_launch_field": strings.Replace(priorBootSchemaFiveCleaning, `"cache_locator":`,
			`"future":1,"cache_locator":`, 1),
		"placement_mismatch": strings.Replace(priorBootSchemaFiveCleaning,
			`"owner_gid":1001,"network_placement":"managed"`,
			`"owner_gid":1001,"network_placement":"networkless"`, 1),
		"missing_execution_identity": strings.Replace(priorBootSchemaFiveCleaning,
			`"execution_identity":{"uid":2000,"gid":2001},`, "", 1),
		"missing_cache_locator": strings.Replace(priorBootSchemaFiveCleaning,
			`"cache_locator":"/home/caller/mvmctl",`, "", 1),
		"duplicate_boot_id": strings.Replace(priorBootSchemaFiveCleaning, `"host_boot_id":`,
			`"host_boot_id":"123e4567-e89b-42d3-a456-426614174002","host_boot_id":`, 1),
		"case_folded_field": strings.Replace(priorBootSchemaFiveCleaning, `"host_boot_id":`, `"HOST_BOOT_ID":`, 1),
		"trailing_value":    priorBootSchemaFiveCleaning + ` {}`,
	}
	for name, raw := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := decodePriorBootRecord([]byte(raw), 1000, key)
			assert.Error(t, err)
		})
	}
}
