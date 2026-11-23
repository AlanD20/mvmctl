package nocloudnet

import (
	"encoding/json"
	"io"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"mvmctl/internal/service/network"
	"mvmctl/pkg/errs"
)

// Rationale: exec hands the child a blocking descriptor, so wrapping an ordinary
// os.Pipe writer directly would miss the failure of both ready and error acknowledgements.
func TestStartupPipeAcknowledgesBlockingInheritedWriter(t *testing.T) {
	tests := map[string]startupResult{
		"startup_error": {Error: "reserved gateway listener is unavailable"},
		"ready_endpoint": {Endpoint: &network.HostServiceResult{
			NetworkID: strings.Repeat("a", 64), Gateway: "10.81.0.1", Port: 8000,
		}},
	}
	for name, want := range tests {
		t.Run(name, func(t *testing.T) {
			reader, writer, err := os.Pipe()
			require.NoError(t, err)
			t.Cleanup(func() { assert.NoError(t, reader.Close()) })
			t.Cleanup(func() { assert.NoError(t, writer.Close()) })
			// Fd performs exec's blocking transition; the duplicate has independent close ownership.
			fd, err := unix.Dup(int(writer.Fd()))
			require.NoError(t, err)
			var ready *os.File
			t.Cleanup(func() {
				if ready != nil {
					assert.NoError(t, ready.Close())
				} else {
					assert.NoError(t, unix.Close(fd))
				}
			})
			flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFL, 0)
			require.NoError(t, err)
			require.Zero(t, flags&unix.O_NONBLOCK, "the inherited writer must start blocking")
			ready, err = openStartupPipe(t.Context(), fd)
			require.NoError(t, err)
			require.NoError(t, reader.SetReadDeadline(time.Now().Add(time.Second)))

			require.NoError(t, writeStartup(ready, want))
			var got startupResult
			require.NoError(t, json.NewDecoder(io.LimitReader(reader, 4096)).Decode(&got))
			if diff := cmp.Diff(want, got); diff != "" {
				t.Errorf("startup acknowledgement mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// Rationale: an unavailable inherited descriptor must retain its OS cause and
// fail before a wrapper can falsely represent a usable readiness channel.
func TestStartupPipeRejectsInvalidDescriptor(t *testing.T) {
	_, err := openStartupPipe(t.Context(), -1)
	require.Error(t, err)
	var domainErr *errs.DomainError
	require.ErrorAs(t, err, &domainErr)
	assert.Equal(t, errs.CodeCloudInitNetModeFailed, domainErr.Code)
	assert.Equal(t, errs.ClassInternal, domainErr.Class)
	assert.ErrorIs(t, err, syscall.EBADF)
}
