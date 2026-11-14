package network

import (
	"context"
	"strings"
	"time"

	"mvmctl/internal/lib/system"
)

// NetOps abstracts read-only bridge and TAP observations. Mutations are owned
// by the authenticated network service, never by this unprivileged helper.
// The default implementation uses DefaultRunner; tests can inject a fake.
//
// Each method matches the corresponding public function in this package.
type NetOps interface {
	BridgeExists(ctx context.Context, bridge string) bool
	TapExists(ctx context.Context, tap string) bool
	BridgeHasSubnet(ctx context.Context, bridge, subnet string) bool
	GetBridgeTaps(ctx context.Context, bridge string) []string
	GetTapBridge(ctx context.Context, tap string) string
	GetBridgeSlaves(ctx context.Context, bridge string) []string
	GetSystemBridges(ctx context.Context) []string
}

// DefaultNetOps is the package-level NetOps provider.
// Swap this in tests to inject a fake.
var DefaultNetOps NetOps = realNetOps{}

// realNetOps implements NetOps via system.DefaultRunner.
type realNetOps struct{}

func (realNetOps) BridgeExists(ctx context.Context, bridge string) bool {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	result, _ := system.DefaultRunner.Run(ctx,
		[]string{"ip", "link", "show", bridge},
		system.RunCmdOpts{Check: false, Capture: true})
	return result.Success()
}

func (realNetOps) TapExists(ctx context.Context, tap string) bool {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	result, _ := system.DefaultRunner.Run(ctx,
		[]string{"ip", "link", "show", tap},
		system.RunCmdOpts{Check: false, Capture: true})
	return result.Success()
}

func (realNetOps) BridgeHasSubnet(ctx context.Context, bridge, subnet string) bool {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	result, _ := system.DefaultRunner.Run(ctx,
		[]string{"ip", "-o", "addr", "show", bridge},
		system.RunCmdOpts{Check: false, Capture: true})
	if !result.Success() {
		return false
	}
	return strings.Contains(result.Stdout, subnet)
}

func (realNetOps) GetBridgeTaps(ctx context.Context, bridge string) []string {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	result, _ := system.DefaultRunner.Run(ctx,
		[]string{"ip", "link", "show", "master", bridge},
		system.RunCmdOpts{Check: false, Capture: true})
	if !result.Success() {
		return nil
	}
	var devices []string
	for line := range strings.SplitSeq(result.Stdout, "\n") {
		parts := strings.Fields(line)
		if len(parts) > 0 && len(parts[0]) > 0 && parts[0][0] >= '0' && parts[0][0] <= '9' && len(parts) >= 2 {
			iface := strings.TrimRight(parts[1], ":")
			devices = append(devices, iface)
		}
	}
	return devices
}

func (realNetOps) GetTapBridge(ctx context.Context, tap string) string {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	result, _ := system.DefaultRunner.Run(ctx,
		[]string{"ip", "link", "show", tap},
		system.RunCmdOpts{Check: false, Capture: true})
	if !result.Success() {
		return ""
	}
	for line := range strings.SplitSeq(result.Stdout, "\n") {
		if strings.Contains(line, "master") {
			parts := strings.Fields(line)
			for i, part := range parts {
				if part == "master" && i+1 < len(parts) {
					return parts[i+1]
				}
			}
		}
	}
	return ""
}

func (realNetOps) GetBridgeSlaves(ctx context.Context, bridge string) []string {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	result, _ := system.DefaultRunner.Run(ctx,
		[]string{"ip", "-o", "link", "show", "master", bridge},
		system.RunCmdOpts{Check: false, Capture: true})
	if !result.Success() {
		return nil
	}
	var slaves []string
	for line := range strings.SplitSeq(result.Stdout, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) >= 2 {
			slave := strings.TrimRight(parts[1], ":")
			slave = strings.SplitN(slave, "@", 2)[0]
			if slave != bridge {
				slaves = append(slaves, slave)
			}
		}
	}
	return slaves
}

func (realNetOps) GetSystemBridges(ctx context.Context) []string {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	result, _ := system.DefaultRunner.Run(ctx,
		[]string{"ip", "-o", "link", "show", "type", "bridge"},
		system.RunCmdOpts{Capture: true, Check: false})
	if !result.Success() {
		return nil
	}
	var bridges []string
	for line := range strings.SplitSeq(result.Stdout, "\n") {
		parts := strings.Fields(line)
		if len(parts) >= 2 {
			bridges = append(bridges, strings.TrimRight(parts[1], ":"))
		}
	}
	return bridges
}
