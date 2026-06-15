package vsock

import (
	"context"
	"crypto/rand"
	"fmt"
	"log/slog"
	"math/big"
	"time"

	"mvmctl/internal/lib/crypto"
	"mvmctl/internal/lib/model"
)

// maxGuestCID is the maximum valid Firecracker guest CID (2^32 - 1).
const maxGuestCID = 4294967295

// Service provides intra-domain orchestration for vsock operations.
type Service struct {
	repo Repository
}

// NewService creates a new vsock Service.
func NewService(repo Repository) *Service {
	return &Service{repo: repo}
}

// AllocateCID generates a random guest CID in the valid range [3, 4294967295].
// CIDs 0 (VMADDR_CID_HYPERVISOR), 1 (VMADDR_CID_LOCAL), and 2 (VMADDR_CID_HOST)
// are reserved by the Linux vsock specification.
func (s *Service) AllocateCID() (int, error) {
	maxCID := big.NewInt(maxGuestCID - 2)
	c, err := rand.Int(rand.Reader, maxCID)
	if err != nil {
		return 0, fmt.Errorf("generate vsock CID: %w", err)
	}
	return int(c.Int64()) + 3, nil
}

// PersistConfig creates a vsock configuration record for a VM using the given CID.
// If the Upsert fails (e.g. unique constraint collision on guest_cid), it retries
// with a fresh random CID up to maxCIDRetries times.
func (s *Service) PersistConfig(ctx context.Context, cid int, vmID, vmName, udsPath string, port int, token string) error {
	const maxCIDRetries = 5

	item := &model.VsockConfigItem{
		ID:       crypto.VMID(vmName, time.Now().Format(time.RFC3339)),
		VmID:     vmID,
		GuestCID: cid,
		UDSPath:  udsPath,
		Port:     port,
		Token:    token,
	}

	for attempt := range maxCIDRetries {
		if attempt > 0 {
			newCID, err := s.AllocateCID()
			if err != nil {
				return err
			}
			item.GuestCID = newCID
		}

		if err := s.repo.Upsert(ctx, item); err != nil {
			slog.Debug("vsock config upsert failed, retrying with new CID",
				"vm", vmName, "attempt", attempt+1, "error", err)
			continue
		}
		return nil
	}

	return fmt.Errorf("persist vsock config after %d retries", maxCIDRetries)
}
