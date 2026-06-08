package testutil

import (
	"context"

	libnet "mvmctl/internal/lib/network"
)

// FakeNetOps implements libnet.NetOps for testing.
// Each method returns the corresponding field value.
type FakeNetOps struct {
	BridgeExistsFn     func(ctx context.Context, bridge string) bool
	TapExistsFn        func(ctx context.Context, tap string) bool
	BridgeHasSubnetFn  func(ctx context.Context, bridge, subnet string) bool
	GetBridgeTapsFn    func(ctx context.Context, bridge string) []string
	GetTapBridgeFn     func(ctx context.Context, tap string) string
	GetBridgeSlavesFn  func(ctx context.Context, bridge string) []string
	GetSystemBridgesFn func(ctx context.Context) []string
	RunBatchFn         func(ctx context.Context, commands []string) error
	RemoveRawTapFn     func(ctx context.Context, tap string) error
	RemoveRawBridgeFn  func(ctx context.Context, bridge string) error
}

func (f *FakeNetOps) BridgeExists(ctx context.Context, bridge string) bool {
	if f.BridgeExistsFn != nil {
		return f.BridgeExistsFn(ctx, bridge)
	}
	return false
}

func (f *FakeNetOps) TapExists(ctx context.Context, tap string) bool {
	if f.TapExistsFn != nil {
		return f.TapExistsFn(ctx, tap)
	}
	return false
}

func (f *FakeNetOps) BridgeHasSubnet(ctx context.Context, bridge, subnet string) bool {
	if f.BridgeHasSubnetFn != nil {
		return f.BridgeHasSubnetFn(ctx, bridge, subnet)
	}
	return false
}

func (f *FakeNetOps) GetBridgeTaps(ctx context.Context, bridge string) []string {
	if f.GetBridgeTapsFn != nil {
		return f.GetBridgeTapsFn(ctx, bridge)
	}
	return nil
}

func (f *FakeNetOps) GetTapBridge(ctx context.Context, tap string) string {
	if f.GetTapBridgeFn != nil {
		return f.GetTapBridgeFn(ctx, tap)
	}
	return ""
}

func (f *FakeNetOps) GetBridgeSlaves(ctx context.Context, bridge string) []string {
	if f.GetBridgeSlavesFn != nil {
		return f.GetBridgeSlavesFn(ctx, bridge)
	}
	return nil
}

func (f *FakeNetOps) GetSystemBridges(ctx context.Context) []string {
	if f.GetSystemBridgesFn != nil {
		return f.GetSystemBridgesFn(ctx)
	}
	return nil
}

func (f *FakeNetOps) RunBatch(ctx context.Context, commands []string) error {
	if f.RunBatchFn != nil {
		return f.RunBatchFn(ctx, commands)
	}
	return nil
}

func (f *FakeNetOps) RemoveRawTap(ctx context.Context, tap string) error {
	if f.RemoveRawTapFn != nil {
		return f.RemoveRawTapFn(ctx, tap)
	}
	return nil
}

func (f *FakeNetOps) RemoveRawBridge(ctx context.Context, bridge string) error {
	if f.RemoveRawBridgeFn != nil {
		return f.RemoveRawBridgeFn(ctx, bridge)
	}
	return nil
}

// Ensure FakeNetOps implements libnet.NetOps.
var _ libnet.NetOps = (*FakeNetOps)(nil)
