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
	"mvmctl/internal/lib/version"
	"mvmctl/pkg/errs"
)

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
	maxCID := big.NewInt(int64(model.MaxVsockGuestCID) - 2)
	c, err := rand.Int(rand.Reader, maxCID)
	if err != nil {
		return 0, fmt.Errorf("generate vsock CID: %w", err)
	}
	return int(c.Int64()) + 3, nil
}

// PersistConfig creates a vsock configuration record for a VM using the given CID.
func (s *Service) PersistConfig(
	ctx context.Context,
	cid int,
	vmID, vmName, udsPath string,
	port int,
	token string,
) error {
	if !model.IsValidVsockGuestCID(cid) {
		err := errs.New(errs.CodeValidationFailed, "vsock guest CID is outside the supported range")
		slog.Error("invalid vsock guest CID", "vm", vmName, "guest_cid", cid, "error", err)
		return err
	}

	agentVersion := version.BuildVersion
	if agentVersion == "" {
		agentVersion = "0.0.0"
	}

	item := &model.VsockConfigItem{
		ID:           crypto.VMID(vmName, time.Now().Format(time.RFC3339)),
		VmID:         vmID,
		GuestCID:     cid,
		UDSPath:      udsPath,
		Port:         port,
		Token:        token,
		AgentVersion: agentVersion,
	}

	if err := s.repo.Upsert(ctx, item); err != nil {
		slog.Error("failed to persist vsock config", "vm", vmName, "guest_cid", cid, "error", err)
		return errs.WrapMsg(
			errs.CodeDatabaseError,
			"failed to persist vsock configuration",
			err,
			errs.WithClass(errs.ClassInternal),
		)
	}
	return nil
}
