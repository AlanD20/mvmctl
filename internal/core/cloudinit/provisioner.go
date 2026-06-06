package cloudinit

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"mvmctl/internal/infra/firewall"
	"mvmctl/internal/infra/model"
	nocloudnetsvc "mvmctl/internal/service/nocloudnet"
)

// Provisioner handles all cloud-init provisioning modes.
// Matches Python's CloudInitProvisioner.
type Provisioner struct {
	config          *Config
	manager         *Manager
	firewallTracker *firewall.FirewallTracker
}

// NewProvisioner creates a new CloudInitProvisioner.
// The tracker parameter is a pre-configured firewall tracker. If nil,
// firewall operations in NET mode are skipped.
func NewProvisioner(config *Config, tracker *firewall.FirewallTracker) *Provisioner {
	return &Provisioner{
		config:          config,
		manager:         NewManager(config),
		firewallTracker: tracker,
	}
}

// Provision performs cloud-init provisioning based on the configured mode.
// Matches Python's provision().
func (p *Provisioner) Provision(ctx context.Context) (*model.CloudInitResult, error) {
	if p.config.Mode == model.CloudInitModeOFF {
		return p.provisionOff(ctx), nil
	}

	// Prepare the cloud-init config directory — Python uses CONST_DIR_PERMS_CACHE = 0o700
	if err := os.MkdirAll(p.config.CloudInitDir, 0700); err != nil {
		return nil, ErrCloudInitProvisionFailed(
			fmt.Sprintf("create cloud-init dir: %s", err))
	}

	// Generate config files
	if err := p.manager.Generate(ctx); err != nil {
		return nil, err
	}

	switch p.config.Mode {
	case model.CloudInitModeNET:
		return p.provisionNet(ctx)
	case model.CloudInitModeISO:
		return p.provisionISO(ctx)
	case model.CloudInitModeINJECT:
		return p.provisionInject(ctx)
	default:
		return nil, fmt.Errorf("unknown cloud-init mode: %s", p.config.Mode)
	}
}

// provisionOff handles OFF mode — cloud-init disabled.
// Matches Python's _provision_off().
// Python: CloudInitmodel.CloudInitResult(mode=CloudInitMode.OFF) -> nocloud_net_rules=[] (factory default)
func (p *Provisioner) provisionOff(ctx context.Context) *model.CloudInitResult {
	return &model.CloudInitResult{Mode: model.CloudInitModeOFF, NocloudNetRules: []model.FirewallRule{}}
}

// provisionNet handles NET mode firewall rules for a single VM.
// Uses the pre-allocated server from config (NoCloudURL/NoCloudPort/NoCloudPID),
// or spawns anew if not pre-allocated (single-VM compatibility path).
func (p *Provisioner) provisionNet(ctx context.Context) (*model.CloudInitResult, error) {

	url := p.config.NoCloudURL
	allocatedPort := p.config.NoCloudPort
	spid := p.config.NoCloudPID

	// If no pre-allocated server, spawn one (e.g. single VM without batch path)
	if url == "" {
		port := 0
		if p.config.NocloudNetPort != nil {
			port = *p.config.NocloudNetPort
		}
		host := p.config.IPv4Gateway

		killAfter := p.config.KillAfter
		if killAfter == 0 {
			killAfter = 5 * time.Minute // default auto-kill to prevent process leaks
		}

		var spawnErr error
		url, allocatedPort, spid, spawnErr = nocloudnetsvc.SpawnNoCloudServer(
			p.config.VMID,
			p.config.VMDir,
			p.config.CloudInitDir,
			host,
			port,
			p.config.NocloudPortRangeStart,
			p.config.NocloudPortRangeEnd,
			killAfter,
		)
		if spawnErr != nil {
			return nil, spawnErr
		}
	}

	// ── Firewall rule creation (matching Python exactly) ──
	if p.firewallTracker == nil {
		slog.Warn("No firewall tracker available, skipping NET mode firewall rules",
			"vm_name", p.config.VMName)
		return &model.CloudInitResult{
			Mode:            model.CloudInitModeNET,
			NocloudURL:      &url,
			NocloudPort:     allocatedPort,
			NocloudPID:      &spid,
			NocloudNetRules: []model.FirewallRule{},
		}, nil
	}

	// Ensure the nocloud chain exists (in filter table)
	_ = p.firewallTracker.EnsureChain(
		ctx,
		model.FirewallChainMVMNocloudNetIn,
		model.FirewallTableFilter,
		"INPUT",
		0,
	)

	commentTag := fmt.Sprintf("# nocloudnet:%s:%d", p.config.VMName, allocatedPort)
	networkName := p.config.NetworkName

	rule := model.FirewallRule{
		TableName:    model.FirewallTableFilter,
		ChainName:    model.FirewallChainMVMNocloudNetIn,
		RuleType:     model.FirewallRuleTypeNocloudNetInput,
		Target:       model.FirewallTargetAccept,
		NetworkID:    p.config.NetworkID,
		Protocol:     model.FirewallProtocolTCP,
		Source:       p.config.GuestIP,
		Destination:  p.config.IPv4Gateway,
		InInterface:  p.config.TapName,
		OutInterface: string(model.FirewallWildcardAnyInterface),
		SPort:        model.FirewallPortAny,
		DPort:        allocatedPort,
		NetworkName:  &networkName,
		CommentTag:   &commentTag,
		IsActive:     true,
	}
	if fwResult := p.firewallTracker.EnsureRule(ctx, rule, "nocloud-net"); !fwResult.Success {
		msg := ""
		if fwResult.ErrorMessage != nil {
			msg = *fwResult.ErrorMessage
		}
		return nil, ErrCloudInitNetModeFailed(
			fmt.Sprintf("Nocloud-net provisioning failed: %s", msg))
	}

	return &model.CloudInitResult{
		Mode:            model.CloudInitModeNET,
		NocloudURL:      &url,
		NocloudPort:     allocatedPort,
		NocloudPID:      &spid,
		NocloudNetRules: []model.FirewallRule{rule},
	}, nil
}

// provisionISO handles ISO mode — create cloud-init ISO image.
// Matches Python's _provision_iso().
func (p *Provisioner) provisionISO(ctx context.Context) (*model.CloudInitResult, error) {
	// Check for pre-existing custom ISO (Python: if self._config.cloud_init_iso_path is not None)
	if p.config.CloudInitISOPath != nil {
		isoPath := *p.config.CloudInitISOPath
		if _, err := os.Stat(isoPath); os.IsNotExist(err) {
			return nil, ErrCloudInitISOModeFailed(
				fmt.Sprintf("Custom cloud-init ISO not found: %s", isoPath),
			)
		}
		return &model.CloudInitResult{
			Mode:            model.CloudInitModeISO,
			ISOPath:         p.config.CloudInitISOPath,
			NocloudNetRules: []model.FirewallRule{},
		}, nil
	}

	// Generate ISO from seed directory
	// Python: except Exception as exc: raise CloudInitIsoModeError(f"Failed to create cloud-init ISO: {exc}") from exc
	isoPath := filepath.Join(p.config.VMDir, p.config.CloudInitISOName)
	if err := p.manager.CreateSeedISO(ctx, p.config.CloudInitDir, isoPath); err != nil {
		return nil, ErrCloudInitISOModeFailed(
			fmt.Sprintf("Failed to create cloud-init ISO: %s", err),
		)
	}

	return &model.CloudInitResult{
		Mode:            model.CloudInitModeISO,
		ISOPath:         &isoPath,
		NocloudNetRules: []model.FirewallRule{},
	}, nil
}

// provisionInject handles INJECT mode — config files already written.
// Matches Python's _provision_inject().
func (p *Provisioner) provisionInject(ctx context.Context) (*model.CloudInitResult, error) {
	return &model.CloudInitResult{Mode: model.CloudInitModeINJECT, NocloudNetRules: []model.FirewallRule{}}, nil
}
