package loopmount

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/pkg/errs"
)

func TestSelectRootfsMountSource(t *testing.T) {
	t.Parallel()

	rawExt4 := rootfsFilesystemProbe{
		identity:   rootfsBlockIdentity{major: 7, minor: 0},
		filesystem: FilesystemExt4,
	}
	partitionOne := rootfsFilesystemProbe{
		identity:  rootfsBlockIdentity{major: 7, minor: 1},
		partition: 1,
	}
	partitionTwoExt4 := rootfsFilesystemProbe{
		identity:   rootfsBlockIdentity{major: 7, minor: 2},
		partition:  2,
		filesystem: FilesystemExt4,
	}
	partitionThreeBtrfs := rootfsFilesystemProbe{
		identity:   rootfsBlockIdentity{major: 7, minor: 3},
		partition:  3,
		filesystem: FilesystemBtrfs,
	}
	maximumPartitions := testRootfsPartitionProbes(maxRootfsPartitionCount)
	maximumPartitions[maxRootfsPartitionCount-1].filesystem = FilesystemExt4
	tests := map[string]struct {
		expected   Filesystem
		raw        rootfsFilesystemProbe
		partitions []rootfsFilesystemProbe
		want       rootfsMountSource
		wantErr    string
	}{
		"raw filesystem": {
			expected: FilesystemExt4,
			raw:      rawExt4,
			want: rootfsMountSource{
				kind: rootfsMountSourceRaw, identity: rawExt4.identity,
			},
		},
		"one matching root partition beside unrecognized boot partition": {
			expected:   FilesystemExt4,
			raw:        rootfsFilesystemProbe{identity: rawExt4.identity},
			partitions: []rootfsFilesystemProbe{partitionOne, partitionTwoExt4},
			want: rootfsMountSource{
				kind: rootfsMountSourcePartition, identity: partitionTwoExt4.identity, partition: 2,
			},
		},
		"one matching partition is sufficient": {
			expected: FilesystemExt4,
			raw:      rootfsFilesystemProbe{identity: rawExt4.identity},
			partitions: []rootfsFilesystemProbe{
				partitionTwoExt4,
			},
			want: rootfsMountSource{
				kind: rootfsMountSourcePartition, identity: partitionTwoExt4.identity, partition: 2,
			},
		},
		"selection follows the requested closed filesystem instead of partition order": {
			expected: FilesystemBtrfs,
			raw:      rootfsFilesystemProbe{identity: rawExt4.identity},
			partitions: []rootfsFilesystemProbe{
				partitionThreeBtrfs,
				partitionTwoExt4,
			},
			want: rootfsMountSource{
				kind: rootfsMountSourcePartition, identity: partitionThreeBtrfs.identity, partition: 3,
			},
		},
		"raw filesystem mismatch": {
			expected: FilesystemBtrfs, raw: rawExt4, wantErr: "does not match",
		},
		"unrecognized raw filesystem": {
			expected: FilesystemExt4,
			raw:      rootfsFilesystemProbe{identity: rawExt4.identity},
			wantErr:  "does not match",
		},
		"raw match with partition layout": {
			expected: FilesystemExt4,
			raw:      rawExt4,
			partitions: []rootfsFilesystemProbe{
				partitionOne,
			},
			wantErr: "raw filesystem and partition layout are ambiguous",
		},
		"no matching partition": {
			expected: FilesystemBtrfs,
			raw:      rootfsFilesystemProbe{identity: rawExt4.identity},
			partitions: []rootfsFilesystemProbe{
				partitionOne,
				partitionTwoExt4,
			},
			wantErr: "has no matching root partition",
		},
		"multiple matching partitions": {
			expected: FilesystemExt4,
			raw:      rootfsFilesystemProbe{identity: rawExt4.identity},
			partitions: []rootfsFilesystemProbe{
				partitionTwoExt4,
				{identity: rootfsBlockIdentity{major: 7, minor: 4}, partition: 4, filesystem: FilesystemExt4},
			},
			wantErr: "has multiple matching root partitions",
		},
		"partition count exceeds bound": {
			expected:   FilesystemExt4,
			raw:        rootfsFilesystemProbe{identity: rawExt4.identity},
			partitions: testRootfsPartitionProbes(maxRootfsPartitionCount + 1),
			wantErr:    "partition count exceeds",
		},
		"partition count at bound": {
			expected:   FilesystemExt4,
			raw:        rootfsFilesystemProbe{identity: rawExt4.identity},
			partitions: maximumPartitions,
			want: rootfsMountSource{
				kind: rootfsMountSourcePartition,
				identity: rootfsBlockIdentity{
					major: 7,
					minor: maxRootfsPartitionCount,
				},
				partition: maxRootfsPartitionCount,
			},
		},
		"raw probe has partition number": {
			expected: FilesystemExt4,
			raw: rootfsFilesystemProbe{
				identity: rawExt4.identity, partition: 1, filesystem: FilesystemExt4,
			},
			wantErr: "raw block-device probe is invalid",
		},
		"raw probe has zero identity": {
			expected: FilesystemExt4,
			raw:      rootfsFilesystemProbe{filesystem: FilesystemExt4},
			wantErr:  "raw block-device probe is invalid",
		},
		"raw probe has zero major": {
			expected: FilesystemExt4,
			raw: rootfsFilesystemProbe{
				identity: rootfsBlockIdentity{minor: 1}, filesystem: FilesystemExt4,
			},
			wantErr: "raw block-device probe is invalid",
		},
		"partition has zero identity": {
			expected: FilesystemExt4,
			raw:      rootfsFilesystemProbe{identity: rawExt4.identity},
			partitions: []rootfsFilesystemProbe{
				{partition: 1, filesystem: FilesystemExt4},
			},
			wantErr: "partition block-device identity is invalid",
		},
		"partition has zero major": {
			expected: FilesystemExt4,
			raw:      rootfsFilesystemProbe{identity: rawExt4.identity},
			partitions: []rootfsFilesystemProbe{
				{identity: rootfsBlockIdentity{minor: 2}, partition: 2, filesystem: FilesystemExt4},
			},
			wantErr: "partition block-device identity is invalid",
		},
		"partition number is zero": {
			expected: FilesystemExt4,
			raw:      rootfsFilesystemProbe{identity: rawExt4.identity},
			partitions: []rootfsFilesystemProbe{
				{identity: partitionTwoExt4.identity, filesystem: FilesystemExt4},
			},
			wantErr: "partition number is outside",
		},
		"partition number exceeds bound": {
			expected: FilesystemExt4,
			raw:      rootfsFilesystemProbe{identity: rawExt4.identity},
			partitions: []rootfsFilesystemProbe{
				{
					identity:   rootfsBlockIdentity{major: 7, minor: 17},
					partition:  maxRootfsPartitionCount + 1,
					filesystem: FilesystemExt4,
				},
			},
			wantErr: "partition number is outside",
		},
		"duplicate partition number": {
			expected: FilesystemExt4,
			raw:      rootfsFilesystemProbe{identity: rawExt4.identity},
			partitions: []rootfsFilesystemProbe{
				partitionTwoExt4,
				{
					identity:   rootfsBlockIdentity{major: 7, minor: 4},
					partition:  2,
					filesystem: FilesystemBtrfs,
				},
			},
			wantErr: "partition number is duplicated",
		},
		"duplicate block identity": {
			expected: FilesystemExt4,
			raw:      rootfsFilesystemProbe{identity: rawExt4.identity},
			partitions: []rootfsFilesystemProbe{
				partitionTwoExt4,
				{identity: partitionTwoExt4.identity, partition: 3, filesystem: FilesystemBtrfs},
			},
			wantErr: "partition block-device identity is duplicated",
		},
		"partition reuses raw block identity": {
			expected: FilesystemExt4,
			raw:      rootfsFilesystemProbe{identity: rawExt4.identity},
			partitions: []rootfsFilesystemProbe{
				{identity: rawExt4.identity, partition: 1, filesystem: FilesystemExt4},
			},
			wantErr: "partition block-device identity matches the raw device",
		},
		"invalid expected filesystem": {
			expected: Filesystem("xfs"), raw: rawExt4, wantErr: "requested rootfs filesystem is invalid",
		},
		"empty expected filesystem": {
			raw: rawExt4, wantErr: "requested rootfs filesystem is invalid",
		},
		"invalid raw probed filesystem": {
			expected: FilesystemExt4,
			raw: rootfsFilesystemProbe{
				identity: rawExt4.identity, filesystem: Filesystem("xfs"),
			},
			wantErr: "raw probed filesystem is invalid",
		},
		"invalid partition probed filesystem": {
			expected: FilesystemExt4,
			raw:      rootfsFilesystemProbe{identity: rawExt4.identity},
			partitions: []rootfsFilesystemProbe{
				{identity: partitionTwoExt4.identity, partition: 2, filesystem: Filesystem("xfs")},
			},
			wantErr: "partition probed filesystem is invalid",
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			before := append([]rootfsFilesystemProbe(nil), test.partitions...)

			got, err := selectRootfsMountSource(test.expected, test.raw, test.partitions)
			if test.wantErr != "" {
				require.Error(t, err)
				assert.ErrorContains(t, err, test.wantErr)
				domainErr := errs.AsDomainError(err)
				require.NotNil(t, domainErr)
				assert.Equal(t, errs.CodeLoopMountError, domainErr.Code)
				assert.Equal(t, errs.ClassInternal, domainErr.Class)
				assert.Equal(t, rootfsMountSource{}, got)
			} else {
				require.NoError(t, err)
				if diff := cmp.Diff(
					test.want,
					got,
					cmp.AllowUnexported(rootfsMountSource{}, rootfsBlockIdentity{}),
				); diff != "" {
					t.Errorf("selectRootfsMountSource() mismatch (-want +got):\n%s", diff)
				}
			}
			if diff := cmp.Diff(
				before,
				test.partitions,
				cmp.AllowUnexported(rootfsFilesystemProbe{}, rootfsBlockIdentity{}),
			); diff != "" {
				t.Errorf("selectRootfsMountSource() mutated partitions (-before +after):\n%s", diff)
			}
		})
	}
}

func testRootfsPartitionProbes(count int) []rootfsFilesystemProbe {
	partitions := make([]rootfsFilesystemProbe, count)
	for index := range partitions {
		partitions[index] = rootfsFilesystemProbe{
			identity:  rootfsBlockIdentity{major: 7, minor: uint32(index + 1)},
			partition: uint8(index + 1),
		}
	}
	return partitions
}
