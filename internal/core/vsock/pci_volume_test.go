package vsock

import (
	"context"
	"io"
	"net"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"

	"mvmctl/internal/lib/model"
	"mvmctl/internal/service/agent"
)

func TestRemovePCIVolumeRequiresMatchingResultAndEOF(t *testing.T) {
	// Rationale: A complete-looking response without EOF cannot authorize host DELETE.
	identity := model.PCIVolumeIdentity{Serial: "123", ExpectedBytes: 4096}
	cases := map[string]struct{ wrongIdentity, refusal, trailing, holdOpen bool }{
		"changed identity":      {wrongIdentity: true},
		"guest refusal":         {refusal: true},
		"trailing response":     {trailing: true},
		"cancelled missing EOF": {holdOpen: true},
		"verified absence":      {},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			probeClient, probeServer := net.Pipe()
			clientConn, serverConn := net.Pipe()
			t.Cleanup(func() { _ = probeServer.Close(); _ = serverConn.Close() })
			calls := 0
			client := &Client{
				item:             &model.VsockConfigItem{Token: "configured"},
				ProbeTimeout:     time.Second,
				skipVersionCheck: true,
				dialFn: func(context.Context, string, int, int) (net.Conn, error) {
					calls++
					if calls == 1 {
						return probeClient, nil
					}
					return clientConn, nil
				},
			}
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			done := make(chan struct{})
			go func() {
				defer close(done)
				defer serverConn.Close()
				var magic [8]byte
				if _, err := io.ReadFull(serverConn, magic[:]); err != nil {
					t.Error(err)
					return
				}
				if string(magic[:]) != agent.PCIVolumeMagic {
					t.Errorf("unexpected guest protocol %q", magic)
					return
				}
				request, err := agent.ReadPCIVolumeRemoveRequest(t.Context(), serverConn)
				if err != nil {
					t.Error(err)
					return
				}
				want := agent.PCIVolumeRemoveRequest{Token: "configured", Volume: identity}
				if diff := cmp.Diff(want, request); diff != "" {
					t.Errorf("closed request (-want +got):\n%s", diff)
				}
				result := agent.PCIVolumeRemoveResult{Volume: identity, Removed: true}
				if tc.wrongIdentity {
					result.Volume.Serial = "999"
				}
				if tc.refusal {
					result.Removed = false
					result.Error = "mounted"
				}
				if err := agent.WritePCIVolumeRemoveResult(t.Context(), serverConn, result); err != nil {
					t.Error(err)
					return
				}
				if tc.trailing {
					_, _ = serverConn.Write([]byte("x"))
				}
				if tc.holdOpen {
					cancel()
					<-ctx.Done()
				}
			}()
			err := client.RemovePCIVolume(ctx, identity)
			if tc.wrongIdentity || tc.refusal || tc.trailing || tc.holdOpen {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("guest exchange leaked")
			}
		})
	}
}
