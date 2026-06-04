// Package api provides the public orchestration layer for all operations.
// All operations are methods on a single Operation struct, matching the
// Go CLI patterns used by kubectl, gh, and kind.
package api

import (
	"context"
	"fmt"
	"path/filepath"

	"mvmctl/internal/core/binary"
	"mvmctl/internal/core/cache"
	"mvmctl/internal/core/config"
	"mvmctl/internal/core/host"
	"mvmctl/internal/core/image"
	"mvmctl/internal/core/kernel"
	"mvmctl/internal/core/key"
	"mvmctl/internal/core/network"
	"mvmctl/internal/core/ssh"
	"mvmctl/internal/core/vm"
	"mvmctl/internal/core/volume"
	"mvmctl/internal/enricher"
	"mvmctl/internal/infra"
	"mvmctl/internal/infra/db"
	"mvmctl/internal/infra/errs"
	"mvmctl/internal/infra/firewall"
	"mvmctl/internal/infra/logging"
	"mvmctl/internal/infra/model"
	"mvmctl/internal/infra/provisioner"
)

// Operation is the single composition root for all API operations.
// Every method receives the full dependency set.
type Operation struct {
	Connection      *db.Handle
	CacheDir        string
	Enr             *enricher.Enricher
	Repos           Repos
	Services        Services
	ProvisionerType provisioner.ProvisionerType
	AuditLog        *logging.AuditLog
}

// Repos bundles all database repositories.
type Repos struct {
	VM      vm.Repository
	Network network.Repository
	Lease   network.LeaseRepository
	Image   image.Repository
	Kernel  kernel.Repository
	Binary  binary.Repository
	Key     key.Repository
	Volume  volume.Repository
	Host    host.Repository
	Config  config.SettingsRepository
}

// Services bundles all domain services.
type Services struct {
	Binary  *binary.Service
	Image   *image.Service
	Kernel  *kernel.Service
	Network *network.Service
	Host    *host.Service
	Config  *config.Service
	Key     *key.Service
	Volume  *volume.Service
	Cache   *cache.Service
	CP      *ssh.CPService
}
type RequiredService struct {
	Name string
	Svc  any
}

// NewOperation creates the single Operation instance with all dependencies wired.
func NewOperation(ctx context.Context, conn *db.Handle, cacheDir string) *Operation {
	sqlDB := conn.DB()

	r := Repos{
		VM:      vm.NewRepository(sqlDB),
		Network: network.NewRepository(sqlDB),
		Lease:   network.NewLeaseRepository(sqlDB),
		Image:   image.NewRepository(sqlDB),
		Kernel:  kernel.NewRepository(sqlDB),
		Binary:  binary.NewRepository(sqlDB),
		Key:     key.NewRepository(sqlDB),
		Volume:  volume.NewRepository(sqlDB),
		Host:    host.NewRepository(sqlDB),
		Config:  config.NewRepository(sqlDB),
	}
	configReg := config.NewConstraintRegistry()
	config.RegisterBuiltinConstraints(configReg)
	// Create a default firewall tracker (nftables, xtcomment enabled).
	// HostInit will replace it with the properly configured tracker once
	// firewall_backend and iptables_xtcomment settings are resolved.
	defaultFwTracker := firewall.NewFirewallTracker(model.FirewallBackendNFTables, true, sqlDB)

	s := Services{
		Network: network.NewService(r.Network, defaultFwTracker),
		Image:   image.NewService(r.Image),
		Kernel:  kernel.NewService(r.Kernel, cacheDir),
		Binary:  binary.NewService(r.Binary, filepath.Join(cacheDir, "bin"), cacheDir),
		Key:     key.NewService(r.Key, infra.GetKeysDir()),
		Host:    host.NewService(r.Host),
		Config:  config.NewService(r.Config, configReg),
		Volume:  volume.NewService(r.Volume),
		Cache:   cache.NewService(cacheDir, infra.GetTempDir()),
		CP:      ssh.NewCPService(),
	}
	// Enforce that all required services are non-nil — fail fast at startup.
	required := []RequiredService{
		{"Config", s.Config}, {"Image", s.Image}, {"Kernel", s.Kernel},
		{"Binary", s.Binary}, {"Network", s.Network}, {"Host", s.Host},
		{"Key", s.Key}, {"Volume", s.Volume}, {"Cache", s.Cache},
	}
	for _, r := range required {
		if r.Svc == nil {
			panic(fmt.Sprintf("service %s is nil — check initialization", r.Name))
		}
	}

	// Resolve provisioner type once at startup.
	provisionerType := provisioner.ProvisionerLoopMount
	guestfsEnabled, _ := s.Config.GetBool(ctx, "settings", "guestfs_enabled")
	if guestfsEnabled {
		provisionerType = provisioner.ProvisionerGuestFS
	}

	return &Operation{
		Connection:      conn,
		CacheDir:        cacheDir,
		Enr:             enricher.New(r.VM, r.Network, r.Lease, r.Image, r.Kernel, r.Binary, r.Volume),
		Repos:           r,
		Services:        s,
		ProvisionerType: provisionerType,
		AuditLog:        logging.NewAuditLog(cacheDir),
	}
}

// emitProgress calls the onProgress callback if non-nil.
func emitProgress(onProgress func(errs.ProgressEvent), phase, status, msg string) {
	if onProgress == nil {
		return
	}
	onProgress(errs.ProgressEvent{Phase: phase, Status: status, Message: msg})
}
