package agent

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func TestReadyStopsBackgroundChildrenBeforeReturning(t *testing.T) {
	testReadyBackgroundChildren(t, "root", os.Geteuid(), os.Getegid())
}

func TestReadyNonrootStopsBackgroundChildrenBeforeReturning(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root credential switching; execute separately in the approved root test runner")
	}
	account := readyNonrootFixtureAccount(t)
	uid, err := strconv.Atoi(account.Uid)
	require.NoError(t, err)
	require.NotZero(t, uid)
	gid, err := strconv.Atoi(account.Gid)
	require.NoError(t, err)
	home, err := os.Stat(account.HomeDir)
	require.NoError(t, err)
	require.True(t, home.IsDir(), "credential fixture needs the account's real home directory")
	testReadyBackgroundChildren(t, account.Username, uid, gid)
}

// readyNonrootFixtureAccount selects an existing login account without changing the runner.
// A usable login shell makes the pre-fix su path reach the same descendant-cleanup assertion.
func readyNonrootFixtureAccount(t *testing.T) *user.User {
	t.Helper()
	passwd, err := os.ReadFile("/etc/passwd")
	require.NoError(t, err)
	for _, line := range strings.Split(string(passwd), "\n") {
		fields := strings.Split(line, ":")
		if len(fields) != 7 || fields[2] == "0" {
			continue
		}
		switch filepath.Base(fields[6]) {
		case "sh", "bash", "dash", "ash", "zsh":
		default:
			continue
		}
		home, err := os.Stat(fields[5])
		if err != nil || !home.IsDir() {
			continue
		}
		account, err := user.Lookup(fields[0])
		require.NoError(t, err)
		return account
	}
	t.Fatal("approved root runner needs an existing non-root login account with a usable home")
	return nil
}

// testReadyBackgroundChildren observes the real guest execution boundary, including numeric credentials.
func testReadyBackgroundChildren(t *testing.T, targetUser string, uid, gid int) {
	t.Helper()
	// Rationale: an early-exiting shell disables CommandContext cancellation after Wait.
	// Observe a real child's pidfd, with a FIFO holding the shell alive until the fixture
	// pins that child. A successful attempt, failed retry, and peer cancellation all own cleanup.
	for _, tc := range []struct {
		name       string
		exit       int
		disconnect bool
	}{
		{name: "failed attempt", exit: 1},
		{name: "peer cancellation", disconnect: true},
		{name: "successful attempt"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gateDir := t.TempDir()
			gatePath := filepath.Join(gateDir, "release-shell")
			require.NoError(t, unix.Mkfifo(gatePath, 0600))
			if targetUser != "root" {
				// Only this test's Go-owned temporary directories gain traversal. The FIFO
				// remains private to the actual target UID used by the guest command.
				require.NoError(t, os.Chmod(filepath.Dir(gateDir), 0711))
				require.NoError(t, os.Chmod(gateDir, 0711))
				require.NoError(t, os.Chown(gatePath, uid, gid))
			}
			gate, err := os.OpenFile(gatePath, os.O_RDWR|unix.O_NONBLOCK, 0)
			require.NoError(t, err)
			t.Cleanup(func() { _ = gate.Close() })

			host, guest := net.Pipe()
			t.Cleanup(func() { _ = host.Close(); _ = guest.Close() })
			require.NoError(t, host.SetDeadline(time.Now().Add(5*time.Second)))
			ctx, cancel := context.WithCancel(t.Context())
			t.Cleanup(cancel)
			done := make(chan struct{})
			quotedGate := "'" + strings.ReplaceAll(gatePath, "'", "'\\''") + "'"
			command := `sleep 3600 >/dev/null 2>&1 & printf '%s\n' "$!"; read -r release < ` +
				quotedGate + "; exit " + strconv.Itoa(tc.exit)
			go func() {
				defer close(done)
				handleReady(ctx, &execRequest{
					ID:      "child-cleanup",
					Type:    requestTypeReady,
					User:    targetUser,
					Timeout: 5,
					Command: command,
				}, guest, &sync.Mutex{})
			}()

			decoder := json.NewDecoder(host)
			var started execResponse
			require.NoError(t, decoder.Decode(&started))
			require.Equal(t, responseTypeStdout, started.Type)
			child, err := strconv.Atoi(strings.TrimSpace(started.Data))
			require.NoError(t, err)
			require.Positive(t, child)
			pidfd, err := unix.PidfdOpen(child, 0)
			require.NoError(t, err)
			t.Cleanup(func() {
				// A regressed implementation must not leak the task-owned child after assertion failure.
				_ = unix.PidfdSendSignal(pidfd, unix.SIGKILL, nil, 0)
				_ = unix.Close(pidfd)
			})
			process, err := os.Stat("/proc/" + strconv.Itoa(child))
			require.NoError(t, err)
			identity, ok := process.Sys().(*syscall.Stat_t)
			require.True(t, ok)
			assert.Equal(t, uint32(uid), identity.Uid, "background command must run as the requested guest UID")

			if tc.disconnect {
				require.NoError(t, host.Close())
			} else {
				_, err := gate.WriteString("continue\n")
				require.NoError(t, err)
				var result execResponse
				require.NoError(t, decoder.Decode(&result))
				assert.Equal(t, responseTypeResult, result.Type)
				assert.Empty(t, result.Error)
				assert.Equal(t, tc.exit, result.Status)
			}
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("readiness attempt did not finish bounded cleanup")
			}
			fds := []unix.PollFd{{Fd: int32(pidfd), Events: unix.POLLIN}}
			count, err := unix.Poll(fds, 1000)
			require.NoError(t, err)
			assert.Equal(t, 1, count, "readiness returned while its background child was still alive")
			assert.NotZero(t, fds[0].Revents&unix.POLLIN, "child pidfd must report process termination")
		})
	}
}
