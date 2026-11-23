package jailer

import (
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/infra"
)

func TestParseProcessStat(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		raw       string
		pid       int
		wantTicks uint64
		wantErr   string
	}{
		"empty":                     {pid: 4242, wantErr: "malformed"},
		"oversized":                 {raw: strings.Repeat("x", maxProcessStatBytes+1), pid: 4242, wantErr: "malformed"},
		"missing command delimiter": {raw: "4242 firecracker S 1 2", pid: 4242, wantErr: "malformed"},
		"truncated fields":          {raw: "4242 (firecracker) S 1 2\n", pid: 4242, wantErr: "incomplete"},
		"noncanonical PID": {
			raw: strings.Replace(testProcessStat(4242, 987654, "S"), "4242 ", "04242 ", 1),
			pid: 4242, wantErr: "PID is invalid",
		},
		"PID mismatch": {raw: testProcessStat(4243, 987654, "S"), pid: 4242, wantErr: "PID does not match"},
		"zero start ticks": {
			raw: testProcessStat(4242, 0, "S"), pid: 4242, wantErr: "start ticks",
		},
		"noncanonical start ticks": {
			raw: strings.Replace(testProcessStat(4242, 987654, "S"), "987654\n", "0987654\n", 1),
			pid: 4242, wantErr: "start ticks",
		},
		"zombie is not live": {raw: testProcessStat(4242, 987654, "Z"), pid: 4242, wantErr: "not live"},
		"embedded closing parenthesis": {
			raw: strings.Replace(testProcessStat(4242, 987654, "S"), "(firecracker)", "(fire) cracker)", 1),
			pid: 4242, wantTicks: 987654,
		},
		"canonical running process": {raw: testProcessStat(4242, 987654, "R"), pid: 4242, wantTicks: 987654},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got, err := parseProcessStat([]byte(tc.raw), tc.pid)
			if tc.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.wantTicks, got)
		})
	}
}

func TestParseProcessStatus(t *testing.T) {
	t.Parallel()

	valid := testProcessStatus(1000, 1001, "1003 1002")
	tests := map[string]struct {
		raw        string
		wantGroups []uint32
		wantErr    string
	}{
		"empty":       {wantErr: "malformed"},
		"oversized":   {raw: strings.Repeat("x", maxProcessStatusBytes+1), wantErr: "malformed"},
		"NUL byte":    {raw: valid + "\x00", wantErr: "malformed"},
		"missing Uid": {raw: strings.Replace(valid, "Uid:\t1000\t1000\t1000\t1000\n", "", 1), wantErr: "incomplete"},
		"missing Groups": {
			raw: strings.Replace(valid, "Groups:\t1003 1002\n", "", 1), wantErr: "incomplete",
		},
		"duplicate Gid": {raw: valid + "Gid:\t1001\t1001\t1001\t1001\n", wantErr: "repeats Gid"},
		"short credential tuple": {
			raw: strings.Replace(valid, "1000\t1000\t1000\t1000", "1000 1000 1000", 1), wantErr: "incomplete",
		},
		"noncanonical credential": {
			raw: strings.Replace(valid, "1000\t1000\t1000\t1000", "01000 1000 1000 1000", 1), wantErr: "noncanonical",
		},
		"duplicate groups": {raw: strings.Replace(valid, "1003 1002", "1002 1002", 1), wantErr: "duplicates"},
		"group sentinel": {
			raw: strings.Replace(valid, "1003 1002", fmt.Sprint(math.MaxUint32)+" 1002", 1), wantErr: "invalid",
		},
		"too many groups":       {raw: processStatusWithGroups(maxProcessSupplementaryGroups + 1), wantErr: "limit"},
		"canonical credentials": {raw: valid, wantGroups: []uint32{1002, 1003}},
		"empty groups": {
			raw: strings.Replace(valid, "Groups:\t1003 1002", "Groups:\t", 1), wantGroups: []uint32{},
		},
	}

	wantUIDs := repeatedProcessCredentials(1000)
	wantGIDs := repeatedProcessCredentials(1001)
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			userIDs, groupIDs, groups, err := parseProcessStatus([]byte(tc.raw))
			if tc.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
				return
			}
			require.NoError(t, err)
			if diff := cmp.Diff(wantUIDs, userIDs, cmp.AllowUnexported(processCredentialIdentity{})); diff != "" {
				t.Errorf("user identity mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(wantGIDs, groupIDs, cmp.AllowUnexported(processCredentialIdentity{})); diff != "" {
				t.Errorf("group identity mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(tc.wantGroups, groups); diff != "" {
				t.Errorf("supplementary groups mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestParseProcessCgroup(t *testing.T) {
	t.Parallel()

	want := infra.CgroupV2Root + "/" + infra.JailerCgroupParent + "/" + testVMID
	tests := map[string]struct {
		raw     string
		want    string
		wantErr string
	}{
		"empty":             {wantErr: "exactly one"},
		"oversized":         {raw: strings.Repeat("x", maxProcessCgroupBytes+1), wantErr: "exactly one"},
		"truncated prefix":  {raw: "0:\n", wantErr: "invalid"},
		"hybrid membership": {raw: "0::/mvmctl/" + testVMID + "\n7:cpu:/legacy\n", wantErr: "exactly one"},
		"deleted membership": {
			raw: "0::/mvmctl/" + testVMID + " (deleted)\n", wantErr: "invalid",
		},
		"wrong hierarchy": {raw: "1::/mvmctl/" + testVMID + "\n", wantErr: "invalid"},
		"relative path":   {raw: "0::mvmctl/" + testVMID + "\n", wantErr: "invalid"},
		"unclean path":    {raw: "0::/mvmctl/../" + testVMID + "\n", wantErr: "invalid"},
		"unified exact":   {raw: "0::/mvmctl/" + testVMID + "\n", want: want},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got, err := parseProcessCgroup([]byte(tc.raw))
			if tc.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func testProcessStat(pid int, startTicks uint64, state string) string {
	return fmt.Sprintf(
		"%d (firecracker) %s 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 %d\n",
		pid,
		state,
		startTicks,
	)
}

func testProcessStatus(uid, gid uint32, groups string) string {
	return fmt.Sprintf(
		"Name:\tprocess\nUid:\t%d\t%d\t%d\t%d\nGid:\t%d\t%d\t%d\t%d\nGroups:\t%s\n",
		uid,
		uid,
		uid,
		uid,
		gid,
		gid,
		gid,
		gid,
		groups,
	)
}

func processStatusWithGroups(count int) string {
	groups := make([]string, count)
	for index := range groups {
		groups[index] = fmt.Sprint(index + 1)
	}
	return testProcessStatus(1000, 1001, strings.Join(groups, " "))
}

func repeatedProcessCredentials(id uint32) processCredentialIdentity {
	return processCredentialIdentity{real: id, effective: id, saved: id, filesystem: id}
}
