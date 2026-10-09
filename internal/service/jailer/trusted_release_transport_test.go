package jailer

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"mvmctl/internal/lib/system"
)

func TestTrustedReleaseSystemExchangeSelectsExportInterruptionOnlyForExport(t *testing.T) {
	// Rationale: Release and unrelated root actions must retain ordinary fatal cancellation.
	for name, tc := range map[string]struct {
		action string
		export bool
	}{
		"export":  {action: VMRootfsExportAction, export: true},
		"release": {action: VMRootfsExportReleaseAction},
		"install": {action: TrustedReleaseInstallAction},
		"other":   {action: VMRootfsExportAction + "-other"},
	} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			transport := &routingTrustedReleaseTransport{}
			exchange := trustedReleaseSystemExchange{transport: transport}
			outcome := exchange.Exchange(ctx, trustedReleaseExchangeRequest{
				action: tc.action, frame: []byte("request"),
			})
			assert.ErrorIs(t, outcome.diagnostic, context.Canceled)
			assert.False(t, outcome.started)
			assert.False(t, outcome.responseEOF)
			assert.Equal(t, tc.export, transport.export)
			assert.Equal(t, !tc.export, transport.ordinary)
		})
	}
}

// routingTrustedReleaseTransport records which named capability receives the request,
// then exercises that capability's real prestart cancellation boundary without sudo.
type routingTrustedReleaseTransport struct {
	export   bool
	ordinary bool
}

func (transport *routingTrustedReleaseTransport) Exchange(
	ctx context.Context,
	request *system.PrivilegedControlRequest,
) system.PrivilegedControlOutcome {
	transport.ordinary = true
	return system.PrivilegedControlTransport{}.Exchange(ctx, request)
}

func (transport *routingTrustedReleaseTransport) ExchangeVMRootfsExport(
	ctx context.Context,
	request *system.PrivilegedControlRequest,
) system.PrivilegedControlOutcome {
	transport.export = true
	return system.PrivilegedControlTransport{}.ExchangeVMRootfsExport(ctx, request)
}
