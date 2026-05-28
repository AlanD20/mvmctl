// Package api provides the public orchestration layer for all operations.
// All operations are methods on a single Operation struct, matching the
// Go CLI patterns used by kubectl, gh, and kind.
package api

import (
	"database/sql"
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
)

// Operation is the single composition root for all API operations.
// Every method receives the full dependency set.
type Operation struct {
	DB       *sql.DB
	CacheDir string
	Enr      *enricher.Enricher
	Repos    Repos
	Services Services
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
func NewOperation(db *sql.DB, cacheDir string) *Operation {
	r := Repos{
		VM:      vm.NewRepository(db),
		Network: network.NewRepository(db),
		Lease:   network.NewLeaseRepository(db),
		Image:   image.NewRepository(db),
		Kernel:  kernel.NewRepository(db),
		Binary:  binary.NewRepository(db),
		Key:     key.NewRepository(db),
		Volume:  volume.NewRepository(db),
		Host:    host.NewRepository(db),
		Config:  config.NewRepository(db),
	}
	s := Services{
		Network: network.NewService(r.Network, db),
		Image:   image.NewService(r.Image, cacheDir),
		Kernel:  kernel.NewService(r.Kernel, cacheDir),
		Binary:  binary.NewService(r.Binary, filepath.Join(cacheDir, "bin"), cacheDir),
		Key:     key.NewService(r.Key, filepath.Join(cacheDir, "keys")),
		Host:    host.NewService(r.Host),
		Config:  config.NewService(r.Config),
		Volume:  volume.NewService(r.Volume),
		Cache:   cache.NewService(cacheDir),
		CP:      ssh.NewCPService(),
	}
	return &Operation{
		DB:       db,
		CacheDir: cacheDir,
		Enr:      enricher.New(r.VM, r.Network, r.Lease, r.Image, r.Kernel, r.Binary, r.Volume),
		Repos:    r,
		Services: s,
	}
}
