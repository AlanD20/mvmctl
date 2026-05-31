// Package api provides the public orchestration layer for all operations.
// All operations are methods on a single Operation struct, matching the
// Go CLI patterns used by kubectl, gh, and kind.
package api

import (
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
	"mvmctl/internal/infra/firewall"
	"mvmctl/internal/infra/model"
)

// Operation is the single composition root for all API operations.
// Every method receives the full dependency set.
type Operation struct {
	Connection *db.Handle
	CacheDir   string
	Enr        *enricher.Enricher
	Repos      Repos
	Services   Services
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

// NewOperation creates the single Operation instance with all dependencies wired.
func NewOperation(conn *db.Handle, cacheDir string) *Operation {
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
		Image:   image.NewService(r.Image, cacheDir),
		Kernel:  kernel.NewService(r.Kernel, cacheDir),
		Binary:  binary.NewService(r.Binary, filepath.Join(cacheDir, "bin"), cacheDir),
		Key:     key.NewService(r.Key, infra.GetKeyDir()),
		Host:    host.NewService(r.Host),
		Config:  config.NewService(r.Config, configReg),
		Volume:  volume.NewService(r.Volume),
		Cache:   cache.NewService(cacheDir, infra.GetTempDir()),
		CP:      ssh.NewCPService(),
	}
	return &Operation{
		Connection: conn,
		CacheDir:   cacheDir,
		Enr:        enricher.New(r.VM, r.Network, r.Lease, r.Image, r.Kernel, r.Binary, r.Volume),
		Repos:      r,
		Services:   s,
	}
}
