package authorityfs

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// This record is written in the production schema-6 shape independently of
// Jailer's encoder, so a writer and reader cannot share the same field bug.
const priorBootSchemaSixCleaning = `{"schema_version":6,"owner_uid":1000,` +
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

func TestDecodePriorBootSchemaSixCleaning(t *testing.T) {
	t.Parallel()
	key, err := NewVMKey("0123456789abcdef0123456789abcdef")
	require.NoError(t, err)

	withOutputs := strings.Replace(priorBootSchemaSixCleaning,
		`"snapshot_id":""`,
		`"snapshot_id":"","output_identity":{"log":{"device":71,"inode":72},`+
			`"metrics":{"device":73,"inode":74}}`, 1)
	for name, tc := range map[string]struct {
		raw       string
		placement string
		outputs   bool
	}{
		"cleaning_from_managed_launch": {raw: priorBootSchemaSixCleaning, placement: "managed"},
		"cleaning_from_networkless_launch": {
			raw: strings.ReplaceAll(priorBootSchemaSixCleaning, `"network_placement":"managed"`,
				`"network_placement":"networkless"`),
			placement: "networkless",
		},
		"cleaning_with_output_identity": {raw: withOutputs, placement: "managed", outputs: true},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := decodePriorBootRecord([]byte(tc.raw), 1000, key)
			require.NoError(t, err)
			assert.Equal(t, uint32(6), got.SchemaVersion)
			assert.Equal(t, uint32(1000), got.OwnerUID)
			assert.Equal(t, key.value, got.VMID)
			assert.Equal(t, tc.placement, got.NetworkPlacement)
			assert.Equal(t, tc.placement, got.Launch.NetworkPlacement)
			assert.Equal(t, "cleaning", got.Lifecycle)
			assert.Equal(t, uint64(1), got.LaunchGeneration)
			assert.Equal(t, uint64(1), got.CleanupGeneration)
			assert.Equal(t, "123e4567-e89b-42d3-a456-426614174001", got.Launch.HostBootID)
			assert.Equal(t, "/home/caller/mvmctl", got.Launch.CacheLocator)
			assert.Equal(t, `{"uid":2000,"gid":2001}`, string(got.Launch.ExecutionIdentity))
			if tc.outputs {
				assert.Equal(t, `{"log":{"device":71,"inode":72},"metrics":{"device":73,"inode":74}}`,
					string(got.Launch.OutputIdentity))
			} else {
				assert.Empty(t, got.Launch.OutputIdentity)
			}
			assert.Empty(t, got.Active)
		})
	}
}

func TestDecodePriorBootSchemaSixCleaningFromActive(t *testing.T) {
	t.Parallel()
	key, err := NewVMKey("0123456789abcdef0123456789abcdef")
	require.NoError(t, err)
	const active = `{"user_ids":{"real":2000,"effective":2000,"saved":2000,"filesystem":2000},` +
		`"group_ids":{"real":2001,"effective":2001,"saved":2001,"filesystem":2001},` +
		`"supplementary_groups":[],"mount_namespace":{"device":41,"inode":42},` +
		`"jail_root":{"device":43,"inode":44},` +
		`"executable":{"device":45,"inode":46,"size_bytes":120,"mode":33261,"uid":0,"gid":0,"links":1,` +
		`"manifest_sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},` +
		`"relay":{"kind":"absent"}}`
	const presentRelay = `{"kind":"present","lifecycle":"running",` +
		`"user_ids":{"real":1000,"effective":1000,"saved":1000,"filesystem":1000},` +
		`"group_ids":{"real":1001,"effective":1001,"saved":1001,"filesystem":1001},` +
		`"supplementary_groups":[],"mount_namespace":{"device":51,"inode":52},` +
		`"root":{"device":53,"inode":54},` +
		`"executable":{"device":55,"inode":56,"size_bytes":120,"mode":33261,"uid":0,"gid":0,"links":1,` +
		`"sha256":"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"}}`
	for _, lifecycle := range []string{"absent", "running", "stopping", "stopped"} {
		t.Run(lifecycle, func(t *testing.T) {
			launchRaw, activeRaw := priorBootSchemaSixCleaning, active
			if lifecycle != "absent" {
				launchRaw = strings.Replace(launchRaw, `"relay":{"kind":"absent"}`,
					`"relay":{"kind":"present","pid":4243,"start_time_ticks":987655}`, 1)
				relayRaw := strings.Replace(presentRelay, `"lifecycle":"running"`,
					`"lifecycle":"`+lifecycle+`"`, 1)
				activeRaw = strings.Replace(activeRaw, `{"kind":"absent"}`, relayRaw, 1)
			}
			raw := strings.TrimSuffix(launchRaw, "}") + `,"active":` + activeRaw + "}"
			got, err := decodePriorBootRecord([]byte(raw), 1000, key)
			require.NoError(t, err)
			assert.Equal(t, uint32(6), got.SchemaVersion)
			// The projection retains active evidence without interpreting its relay lifecycle.
			assert.Equal(t, activeRaw, string(got.Active))
		})
	}
}

func TestDecodePriorBootRecordRejectsInvalidBinding(t *testing.T) {
	t.Parallel()
	key, err := NewVMKey("0123456789abcdef0123456789abcdef")
	require.NoError(t, err)

	tests := map[string]string{
		"schema_three": strings.Replace(priorBootSchemaSixCleaning, `"schema_version":6`, `"schema_version":3`, 1),
		"schema_four":  strings.Replace(priorBootSchemaSixCleaning, `"schema_version":6`, `"schema_version":4`, 1),
		"schema_five":  strings.Replace(priorBootSchemaSixCleaning, `"schema_version":6`, `"schema_version":5`, 1),
		"schema_seven": strings.Replace(priorBootSchemaSixCleaning, `"schema_version":6`, `"schema_version":7`, 1),
		"unknown_record_field": strings.Replace(priorBootSchemaSixCleaning, `"lifecycle":"cleaning"`,
			`"future":1,"lifecycle":"cleaning"`, 1),
		"unknown_launch_field": strings.Replace(priorBootSchemaSixCleaning, `"cache_locator":`,
			`"future":1,"cache_locator":`, 1),
		"placement_mismatch": strings.Replace(priorBootSchemaSixCleaning,
			`"owner_gid":1001,"network_placement":"managed"`,
			`"owner_gid":1001,"network_placement":"networkless"`, 1),
		"missing_execution_identity": strings.Replace(priorBootSchemaSixCleaning,
			`"execution_identity":{"uid":2000,"gid":2001},`, "", 1),
		"missing_cache_locator": strings.Replace(priorBootSchemaSixCleaning,
			`"cache_locator":"/home/caller/mvmctl",`, "", 1),
		"duplicate_boot_id": strings.Replace(priorBootSchemaSixCleaning, `"host_boot_id":`,
			`"host_boot_id":"123e4567-e89b-42d3-a456-426614174002","host_boot_id":`, 1),
		"case_folded_field": strings.Replace(priorBootSchemaSixCleaning, `"host_boot_id":`, `"HOST_BOOT_ID":`, 1),
		"trailing_value":    priorBootSchemaSixCleaning + ` {}`,
	}
	for name, raw := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := decodePriorBootRecord([]byte(raw), 1000, key)
			assert.Error(t, err)
		})
	}
}
