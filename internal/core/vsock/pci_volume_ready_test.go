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

func TestEnsurePCIVolumeReadyRequiresMatchingResultAndEOF(t *testing.T) {
	// Rationale: A rescan, wrong disk, or apparently successful but incomplete response cannot complete attach.
	identity := model.PCIVolumeIdentity{Serial: "123", ExpectedBytes: 4096}
	for name, tc := range map[string]struct {
		wrongSerial, wrongCapacity, missingReady, refusal, trailing, holdOpen, truncated bool
	}{
		"changed serial":                {wrongSerial: true},
		"changed capacity":              {wrongCapacity: true},
		"rescan only":                   {missingReady: true},
		"guest refusal with ready flag": {refusal: true},
		"trailing response":             {trailing: true},
		"cancelled missing EOF":         {holdOpen: true},
		"truncated result":              {truncated: true},
		"verified exact readiness":      {},
	} {
		t.Run(name, func(t *testing.T) {
			probeClient, probeServer := net.Pipe()
			clientConn, serverConn := net.Pipe()
			t.Cleanup(func() { _ = probeServer.Close(); _ = serverConn.Close() })
			calls := 0
			client := &Client{
				item:         &model.VsockConfigItem{Token: "configured"},
				ProbeTimeout: time.Second, skipVersionCheck: true,
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
				if string(magic[:]) != agent.PCIVolumeReadyMagic {
					t.Errorf("unexpected guest protocol %q", magic)
					return
				}
				request, err := agent.ReadPCIVolumeReadyRequest(ctx, serverConn)
				if err != nil {
					t.Error(err)
					return
				}
				// The emitted request is the closed identity contract, not an implementation call sequence.
				want := agent.PCIVolumeReadyRequest{Token: "configured", Volume: identity}
				if diff := cmp.Diff(want, request); diff != "" {
					t.Errorf("closed readiness request (-want +got):\n%s", diff)
				}
				if tc.truncated {
					_, _ = serverConn.Write([]byte{0, 0, 0, 4, '{'})
					return
				}
				result := agent.PCIVolumeReadyResult{Volume: identity, Ready: true}
				if tc.wrongSerial {
					result.Volume.Serial = "999"
				}
				if tc.wrongCapacity {
					result.Volume.ExpectedBytes = 8192
				}
				if tc.missingReady {
					result.Ready = false
				}
				if tc.refusal {
					result.Error = "driver unavailable"
				}
				if err := agent.WritePCIVolumeReadyResult(ctx, serverConn, result); err != nil {
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
			err := client.EnsurePCIVolumeReady(ctx, identity)
			if tc.wrongSerial || tc.wrongCapacity || tc.missingReady || tc.refusal || tc.trailing || tc.holdOpen ||
				tc.truncated {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("guest readiness exchange leaked")
			}
		})
	}
}
