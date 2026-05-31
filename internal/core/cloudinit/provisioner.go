package cloudinit

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"mvmctl/internal/infra/firewall"
	"mvmctl/internal/infra/model"
	"mvmctl/internal/service/nocloudnet"
)

// Provisioner handles all cloud-init provisioning modes.
// Matches Python's CloudInitProvisioner.
type Provisioner struct {
	config          *model.ProvisionConfig
	manager         *Manager
	firewallTracker *firewall.FirewallTracker
}

// NewProvisioner creates a new CloudInitProvisioner.
// The tracker parameter is a pre-configured firewall tracker. If nil,
// firewall operations in NET mode are skipped.
func NewProvisioner(config *model.ProvisionConfig, tracker *firewall.FirewallTracker) *Provisioner {
	return &Provisioner{
		config:          config,
		manager:         NewManager(config),
		firewallTracker: tracker,
	}
}

// Provision performs cloud-init provisioning based on the configured mode.
// Matches Python's provision().
func (p *Provisioner) Provision(ctx context.Context) (*model.ProvisionResult, error) {
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
// Python: CloudInitmodel.ProvisionResult(mode=CloudInitMode.OFF) -> nocloud_net_rules=[] (factory default)
func (p *Provisioner) provisionOff(ctx context.Context) *model.ProvisionResult {
	return &model.ProvisionResult{Mode: model.CloudInitModeOFF, NocloudNetRules: []model.FirewallRule{}}
}

// provisionNet handles NET mode — nocloud-net HTTP server.
// Matches Python's _provision_net() exactly, including firewall rule creation.
// Python wraps the entire body in try/except Exception as exc → raise CloudInitNetModeError(...) from exc.
// Python creates FirewallTracker(Database()) internally — Go matches by creating it from p.db.
func (p *Provisioner) provisionNet(ctx context.Context) (*model.ProvisionResult, error) {

	// Determine port: use pre-allocated or 0 for auto-allocation
	// Python: port = self._config.nocloud_net_port if self._config.nocloud_net_port is not None else 0
	port := 0
	if p.config.NocloudNetPort != nil {
		port = *p.config.NocloudNetPort
	}

	// Determine host to bind to
	host := p.config.IPv4Gateway
	if host == "" {
		host = "0.0.0.0"
	}

	// Create NoCloudServer with port range for auto-allocation
	// Matches Python's NoCloudNetServerManager construction
	// TODO(verdict #32): Consider using internal/service/nocloudnet/ instead.
	nocloudServer := nocloudnet.NewNoCloudServer(
		p.config.VMID,
		p.config.VMName, // name (Python: name=self._config.vm_name)
		p.config.VMDir,  // path (Python: path=self._config.vm_dir)
		host,            // ipv4_gateway
		port,            // port (0 for auto-allocation)
		p.config.NocloudPortRangeStart,
		p.config.NocloudPortRangeEnd,
		p.config.NocloudMaxPortRetries,
	)

	// Start the server, serving files from cloud-init directory
	// Python's start() returns (url, port, pid)
	url, allocatedPort, spid, err := nocloudServer.Start(ctx, p.config.CloudInitDir)
	if err != nil {
		return nil, ErrCloudInitNetModeFailed(
			fmt.Sprintf("Nocloud-net provisioning failed: %s", err))
	}

	slog.Info("Started NoCloud-net server for VM",
		"vm_name", p.config.VMName,
		"host", host,
		"port", allocatedPort,
		"pid", spid,
	)

	// ── Firewall rule creation (matching Python exactly) ──
	// Python creates FirewallTracker internally:
	//   tracker = FirewallTracker(Database())
	//   tracker.ensure_chain(FirewallChain.MVM_NOCLOUDNET_INPUT, auto_jump_from="INPUT")
	//   tracker.ensure_rule(nocloud_net_in_rule)
	// The tracker is now injected by the caller instead.
	if p.firewallTracker == nil {
		slog.Warn("No firewall tracker available, skipping NET mode firewall rules",
			"vm_name", p.config.VMName)
		return &model.ProvisionResult{
			Mode:              model.CloudInitModeNET,
			NocloudURL:        &url,
			NocloudPort:       allocatedPort,
			NocloudPID:        &spid,
			NocloudNetManager: nocloudServer,
			NocloudNetRules:   []model.FirewallRule{},
		}, nil
	}

	// Ensure the nocloud chain exists (in filter table)
	// Python: tracker.ensure_chain(FirewallChain.MVM_NOCLOUDNET_INPUT, auto_jump_from="INPUT")
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

	return &model.ProvisionResult{
		Mode:              model.CloudInitModeNET,
		NocloudURL:        &url,
		NocloudPort:       allocatedPort,
		NocloudPID:        &spid,
		NocloudNetManager: nocloudServer,
		NocloudNetRules:   []model.FirewallRule{rule},
	}, nil
}

// provisionISO handles ISO mode — create cloud-init ISO image.
// Matches Python's _provision_iso().
func (p *Provisioner) provisionISO(ctx context.Context) (*model.ProvisionResult, error) {
	// Check for pre-existing custom ISO (Python: if self._config.cloud_init_iso_path is not None)
	if p.config.CloudInitISOPath != nil {
		isoPath := *p.config.CloudInitISOPath
		if _, err := os.Stat(isoPath); os.IsNotExist(err) {
			return nil, ErrCloudInitISOModeFailed(
				fmt.Sprintf("Custom cloud-init ISO not found: %s", isoPath),
			)
		}
		return &model.ProvisionResult{
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

	return &model.ProvisionResult{
		Mode:            model.CloudInitModeISO,
		ISOPath:         &isoPath,
		NocloudNetRules: []model.FirewallRule{},
	}, nil
}

// provisionInject handles INJECT mode — config files already written.
// Matches Python's _provision_inject().
func (p *Provisioner) provisionInject(ctx context.Context) (*model.ProvisionResult, error) {
	return &model.ProvisionResult{Mode: model.CloudInitModeINJECT, NocloudNetRules: []model.FirewallRule{}}, nil
}
