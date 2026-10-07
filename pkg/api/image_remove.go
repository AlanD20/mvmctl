package api

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"syscall"
	"time"

	"mvmctl/internal/service/jailer"
	"mvmctl/pkg/api/inputs"
	"mvmctl/pkg/errs"
)

var exactVMImageExportID = regexp.MustCompile(`^[0-9a-f]{64}$`)

// This fixed read-only presence hint avoids enrolling local-only image removal.
// Presence, unsafe metadata or lookup failures confer no deletion authority.
func hasVMImageExportData(ctx context.Context, imageID string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if !exactVMImageExportID.MatchString(imageID) {
		return false, nil
	}
	copy := jailer.VMRootfsExportResult{OwnerUID: uint32(os.Getuid()), ImageID: imageID}
	entryPath := filepath.Dir(copy.CopyPath())
	_, err := os.Lstat(entryPath)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		_, err = os.Lstat(entryPath + ".copy")
		if err == nil {
			return true, nil
		}
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
	}
	if errors.Is(err, os.ErrPermission) {
		// An unenrolled/older host can have the existing root-private state base.
		// Export creates deliberate 0711 traversal before allocating any data. A
		// non-traversable safe legacy base is only a no-readable-export hint;
		// it never means that root metadata or bytes were removed.
		base, baseErr := os.Lstat("/var/lib/mvmctl")
		if baseErr == nil {
			stat, ok := base.Sys().(*syscall.Stat_t)
			if ok && stat.Uid == 0 && stat.Gid == 0 && base.IsDir() && base.Mode().Perm() == 0700 {
				return false, nil
			}
		}
	}
	return false, errs.WrapMsg(errs.CodeImageImportFailed, "cannot inspect VM image export presence", err)
}

func releaseVMImageExport(
	ctx context.Context,
	client *jailer.VMRootfsExportClient,
	request jailer.VMRootfsExportReleaseRequest,
) (jailer.VMRootfsExportReleaseResult, error) {
	recovery, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	return client.Release(recovery, request)
}

func releasePresentVMImageExport(ctx context.Context, imageID string) error {
	present, err := hasVMImageExportData(ctx, imageID)
	if err != nil || !present {
		return err
	}
	result, err := releaseVMImageExport(
		ctx,
		jailer.NewVMRootfsExportClient(),
		jailer.VMRootfsExportReleaseRequest{ImageID: imageID},
	)
	if err != nil {
		return err
	}
	if result.SourceRecoveryPending {
		return errs.New(
			errs.CodeImageImportFailed,
			"export copy released; source VM recovery remains pending",
			errs.WithDetails(map[string]any{"image_id": imageID, "source_vm_id": result.SourceVMID}),
		)
	}
	return nil
}

// A nil batch delegates to ordinary selectors/removal, including absent exports.
func (op *Operation) imageRemoveUnrecordedExport(
	ctx context.Context,
	input inputs.ImageInput,
	force bool,
) *errs.BatchResult {
	id, err := input.ResolveUnrecordedExportRemoval(ctx, op.Repos.Image, force)
	if id == "" && err == nil {
		return nil
	}
	if err == nil {
		var present bool
		present, err = hasVMImageExportData(ctx, id)
		if err == nil && !present {
			return nil
		} // Absence stays local; a sentinel only hints, never authorizes release.
	}
	var released jailer.VMRootfsExportReleaseResult
	if err == nil {
		released, err = releaseVMImageExport(
			ctx,
			jailer.NewVMRootfsExportClient(),
			jailer.VMRootfsExportReleaseRequest{ImageID: id},
		)
	}
	if err != nil {
		domain := errs.AsDomainError(err)
		if domain == nil {
			domain = errs.Wrap(errs.CodeDatabaseError, err)
		}
		return &errs.BatchResult{
			Items: []errs.OperationResult{
				{Status: "error", Code: string(domain.Code), Item: id, Message: domain.Error(), Exception: domain},
			},
		}
	}
	if !released.Found {
		return nil
	}
	warnings := []string{}
	if released.SourceRecoveryPending {
		warnings = append(
			warnings,
			fmt.Sprintf(
				"Source VM %s still requires state recovery or normal termination; only the independent copy was removed.",
				released.SourceVMID,
			),
		)
	}
	op.AuditLog.LogOperation("image.export.remove", map[string]any{"id": id, "source_vm_id": released.SourceVMID}, "")
	return &errs.BatchResult{Items: []errs.OperationResult{{Status: "success", Code: "image.export_removed", Item: id,
		Message:  fmt.Sprintf("Removed unrecorded image export %s; caller files and protected source retained", id),
		Metadata: map[string]any{"export_only": true, "source_vm_id": released.SourceVMID}, Warnings: warnings}}}
}
