package network

import (
	"context"
	"fmt"
	"strings"
	"time"

	"mvmctl/internal/lib/system"
	"mvmctl/pkg/errs"
)

// NetOps abstracts bridge and TAP operations that require system calls
// (ip link, ip tuntap, etc.). The default implementation uses DefaultRunner.
// Tests can inject a fake to avoid needing root access.
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
	RunBatch(ctx context.Context, commands []string) error
	RemoveRawTap(ctx context.Context, tap string) error
	RemoveRawBridge(ctx context.Context, bridge string) error
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

func (realNetOps) RunBatch(ctx context.Context, commands []string) error {
	batch := strings.Join(commands, "\n") + "\n"
	result, err := system.DefaultRunner.Run(ctx, []string{"ip", "-batch", "-"}, system.RunCmdOpts{
		Check:      true,
		Capture:    true,
		Input:      batch,
		Privileged: true,
	})
	if err != nil {
		return fmt.Errorf("ip -batch failed: %w\n%s", err, result.Stderr)
	}
	return nil
}

func (realNetOps) RemoveRawTap(ctx context.Context, tap string) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	result, _ := system.DefaultRunner.Run(ctx, []string{"ip", "link", "delete", tap},
		system.RunCmdOpts{Capture: true, Privileged: true, Check: false})
	if result.Success() {
		return nil
	}
	stderrFirst := strings.TrimSpace(result.Stderr)
	result, _ = system.DefaultRunner.Run(ctx, []string{"ip", "tuntap", "del", "dev", tap, "mode", "tap"},
		system.RunCmdOpts{Capture: true, Privileged: true, Check: false})
	if result.Success() {
		return nil
	}
	details := ""
	if stderrFirst != "" {
		details = fmt.Sprintf(" (%s)", stderrFirst)
	}
	return errs.Wrap(errs.CodeNetworkBridgeFailed,
		fmt.Errorf("failed to remove TAP device '%s': tried 'ip link delete'%s and 'ip tuntap del'", tap, details))
}

func (realNetOps) RemoveRawBridge(ctx context.Context, bridge string) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	for _, slave := range DefaultNetOps.GetBridgeSlaves(ctx, bridge) {
		system.DefaultRunner.Run(ctx, []string{"ip", "link", "set", slave, "down"},
			system.RunCmdOpts{Capture: true, Privileged: true, Check: false})
		result, _ := system.DefaultRunner.Run(ctx, []string{"ip", "link", "delete", slave},
			system.RunCmdOpts{Capture: true, Privileged: true, Check: false})
		if !result.Success() {
			system.DefaultRunner.Run(ctx, []string{"ip", "tuntap", "del", "dev", slave, "mode", "tap"},
				system.RunCmdOpts{Capture: true, Privileged: true, Check: false})
		}
	}
	system.DefaultRunner.Run(ctx, []string{"ip", "link", "set", bridge, "down"},
		system.RunCmdOpts{Capture: true, Privileged: true, Check: false})
	result, _ := system.DefaultRunner.Run(ctx, []string{"ip", "link", "delete", bridge, "type", "bridge"},
		system.RunCmdOpts{Capture: true, Privileged: true, Check: false})
	if result.Success() {
		return nil
	}
	stderrFirst := strings.TrimSpace(result.Stderr)
	result, _ = system.DefaultRunner.Run(ctx, []string{"ip", "link", "delete", bridge},
		system.RunCmdOpts{Capture: true, Privileged: true, Check: false})
	if result.Success() {
		return nil
	}
	details := ""
	if stderrFirst != "" {
		details = fmt.Sprintf(" (%s)", stderrFirst)
	}
	return errs.Wrap(errs.CodeNetworkBridgeFailed,
		fmt.Errorf("failed to remove bridge '%s': tried 'ip link delete' with type%s and without", bridge, details))
}
