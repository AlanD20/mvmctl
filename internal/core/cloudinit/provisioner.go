// Package cloudinit provides cloud-init user data generation for VM provisioning.
// Layer: Core domain — never imports other core/* packages.
package cloudinit

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"mvmctl/internal/infra"
	"mvmctl/internal/lib/model"
	nocloudnetsvc "mvmctl/internal/service/nocloudnet"
	"mvmctl/pkg/errs"
)

// Default auto-kill timeout for spawned nocloud-net server subprocess.
const defaultNocloudKillAfter = 5 * time.Minute

// Provisioner handles all cloud-init provisioning modes.
type Provisioner struct {
	config  *Config
	manager *Manager
}

// NewProvisioner creates a new CloudInitProvisioner.
func NewProvisioner(config *Config) *Provisioner {
	return &Provisioner{
		config:  config,
		manager: NewManager(config),
	}
}

// Provision performs cloud-init provisioning based on the configured mode.
func (p *Provisioner) Provision(ctx context.Context) (*model.CloudInitResult, error) {
	if p.config.Mode == model.CloudInitModeOFF {
		return p.provisionOff(ctx), nil
	}

	// Prepare the cloud-init config directory with restricted permissions.
	if err := os.MkdirAll(p.config.CloudInitDir, 0700); err != nil {
		return nil, errs.New(errs.CodeCloudInitProvisionFailed,
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
func (p *Provisioner) provisionOff(_ context.Context) *model.CloudInitResult {
	return &model.CloudInitResult{Mode: model.CloudInitModeOFF}
}

// provisionNet consumes a batch listener or starts an independently owned NoCloud listener.
func (p *Provisioner) provisionNet(ctx context.Context) (*model.CloudInitResult, error) {
	if p.config.NocloudNetPort != nil {
		return nil, errs.New(
			errs.CodeCloudInitNetModeFailed,
			"custom NoCloud ports are unsupported; the managed gateway reserves TCP 8000",
		)
	}
	if p.config.NoCloudURL == "" {
		killAfter := p.config.KillAfter
		if killAfter == 0 {
			killAfter = defaultNocloudKillAfter
		}

		result, err := nocloudnetsvc.Spawn(ctx, nocloudnetsvc.Config{
			CloudInitDir: p.config.CloudInitDir,
			NetworkID:    p.config.NetworkID,
			LogFile:      filepath.Join(p.config.VMDir, "nocloud-server.log"),
			KillAfter:    killAfter,
		})
		if err != nil {
			return nil, err
		}

		// Store in config so subsequent calls (e.g. respawn) reuse the same server
		p.config.NoCloudURL = result.URL
		p.config.NoCloudPort = result.Port
		p.config.NoCloudPID = result.PID
	}

	if p.config.NoCloudPort != infra.NoCloudNetPort {
		return nil, errs.New(errs.CodeCloudInitNetModeFailed, "NoCloud listener must use the reserved gateway port")
	}

	return &model.CloudInitResult{
		Mode:        model.CloudInitModeNET,
		NocloudURL:  &p.config.NoCloudURL,
		NocloudPort: p.config.NoCloudPort,
		NocloudPID:  &p.config.NoCloudPID,
	}, nil
}

// provisionISO handles ISO mode — create cloud-init ISO image.
func (p *Provisioner) provisionISO(ctx context.Context) (*model.CloudInitResult, error) {
	// Check for pre-existing custom ISO path
	if p.config.CloudInitISOPath != nil {
		sourcePath := *p.config.CloudInitISOPath
		sourceInfo, err := os.Stat(sourcePath)
		if os.IsNotExist(err) {
			return nil, errs.New(errs.CodeCloudInitISOModeFailed,
				fmt.Sprintf("custom cloud-init ISO not found: %s", sourcePath),
			)
		}
		if err != nil {
			return nil, errs.New(
				errs.CodeCloudInitISOModeFailed,
				fmt.Sprintf("inspect custom cloud-init ISO: %s", err),
			)
		}

		isoPath := filepath.Join(p.config.VMDir, infra.VMCloudInitISOFilename)
		if destinationInfo, statErr := os.Stat(isoPath); statErr == nil && os.SameFile(sourceInfo, destinationInfo) {
			return &model.CloudInitResult{
				Mode:    model.CloudInitModeISO,
				ISOPath: &isoPath,
			}, nil
		}
		if err := infra.CopyFile(sourcePath, isoPath); err != nil {
			return nil, errs.New(
				errs.CodeCloudInitISOModeFailed,
				fmt.Sprintf("copy custom cloud-init ISO to managed path: %s", err),
			)
		}
		return &model.CloudInitResult{
			Mode:    model.CloudInitModeISO,
			ISOPath: &isoPath,
		}, nil
	}

	// Generate ISO from seed directory
	isoPath := filepath.Join(p.config.VMDir, infra.VMCloudInitISOFilename)
	if err := p.manager.CreateSeedISO(ctx, p.config.CloudInitDir, isoPath); err != nil {
		return nil, errs.New(errs.CodeCloudInitISOModeFailed,
			fmt.Sprintf("Failed to create cloud-init ISO: %s", err),
		)
	}

	return &model.CloudInitResult{
		Mode:    model.CloudInitModeISO,
		ISOPath: &isoPath,
	}, nil
}

// provisionInject handles INJECT mode — config files already written.
func (p *Provisioner) provisionInject(_ context.Context) (*model.CloudInitResult, error) {
	return &model.CloudInitResult{Mode: model.CloudInitModeINJECT}, nil
}
