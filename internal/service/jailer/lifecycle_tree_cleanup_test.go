package jailer

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"golang.org/x/sys/unix"
)

func TestStoppedJailDirectoryAdmitsOnlyExactJailerPrivateShape(t *testing.T) {
	isolated := vmExecutionIdentity{uid: 12345, gid: 12346}
	preSeal := instanceRecord{launch: &instanceLaunchRecord{executionIdentity: isolated}}
	active := preSeal
	active.active = &instanceActiveIdentity{}
	tests := map[string]struct {
		kind   jailCleanupDirectory
		mode   uint32
		uid    uint32
		gid    uint32
		active bool
		want   bool
	}{
		"root before Jailer": {kind: jailCleanupRoot, mode: 0755, want: true},
		"root after Jailer":  {kind: jailCleanupRoot, mode: 0700, uid: isolated.uid, gid: isolated.gid, want: true},
		"active root must be sealed": {
			kind: jailCleanupRoot, mode: 0700, uid: isolated.uid, gid: isolated.gid, active: true,
		},
		"inactive root after partial sealing": {
			kind: jailCleanupRoot, mode: 0755, uid: isolated.uid, gid: isolated.gid, want: true,
		},
		"active root cannot retain partial sealing": {
			kind: jailCleanupRoot, mode: 0755, uid: isolated.uid, gid: isolated.gid, active: true,
		},
		"partial sealing cannot admit foreign owner": {
			kind: jailCleanupRoot, mode: 0755, uid: isolated.uid + 1, gid: isolated.gid,
		},
		"partial sealing cannot admit foreign group": {
			kind: jailCleanupRoot, mode: 0755, uid: isolated.uid, gid: isolated.gid + 1,
		},
		"Jailer run": {kind: jailCleanupRun, mode: 0700, uid: isolated.uid, gid: isolated.gid, want: true},
		"Jailer devices": {
			kind: jailCleanupDevices, mode: 0700, uid: isolated.uid, gid: isolated.gid, want: true,
		},
		"Jailer device net": {
			kind: jailCleanupDeviceNet, mode: 0700, uid: isolated.uid, gid: isolated.gid, want: true,
		},
		"fixed mvm must remain root":  {kind: jailCleanupMVM, mode: 0700, uid: isolated.uid, gid: isolated.gid},
		"foreign owner":               {kind: jailCleanupRun, mode: 0700, uid: isolated.uid + 1, gid: isolated.gid},
		"group-writable private root": {kind: jailCleanupRoot, mode: 0770, uid: isolated.uid, gid: isolated.gid},
		"runtime remains root":        {kind: jailCleanupRuntime, mode: 0700, want: true},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			record := preSeal
			if tc.active {
				record = active
			}
			stat := unix.Stat_t{Mode: unix.S_IFDIR | tc.mode, Uid: tc.uid, Gid: tc.gid}
			assert.Equal(t, tc.want, stoppedJailDirectoryAdmitted(tc.kind, stat, record))
		})
	}
}
