package image

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"mvmctl/internal/infra"
	"mvmctl/pkg/errs"
)

// ResumeStagedImagePath reopens only an existing local import's exact retained optimization intent.
// Caller-owned recovery describes a retry; the unchanged root protocol re-admits its own durable authority.
func (s *Service) ResumeStagedImagePath(ctx context.Context, imageID string) (_ string, returnErr error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	cacheDir, err := infra.GetCacheDir()
	if err != nil {
		return "", err
	}
	cache, err := os.OpenRoot(cacheDir)
	if err != nil {
		return "", errs.WrapMsg(errs.CodeImageImportFailed, "open retained import cache", err)
	}
	defer func() { returnErr = errors.Join(returnErr, cache.Close()) }()
	retained, err := cache.OpenRoot(filepath.Join("images", "staging", imageID))
	if err != nil {
		return "", errs.WrapMsg(errs.CodeImageImportFailed, "no retained optimization exists for this image ID", err)
	}
	record, _, readErr := readStagedRecovery(ctx, retained)
	closeErr := retained.Close()
	if readErr != nil || closeErr != nil {
		return "", errs.WrapMsg(errs.CodeImageImportFailed, "retained optimization manifest is unavailable or unsafe",
			errors.Join(readErr, closeErr))
	}
	if err := record.SourceFilesystem.Validate(); err != nil {
		return "", err
	}
	path := record.SourcePath
	base := filepath.Join(cacheDir, "images", imageID)
	if path != base+".img" && path != base+"."+string(record.SourceFilesystem) {
		return "", errs.New(errs.CodeImageImportFailed, "retained source is not this image's managed extracted file")
	}
	filesystem, err := s.resolveFSType(ctx, path)
	if err != nil {
		return "", err
	}
	stage, err := openStagedImage(ctx, path, imageID, filesystem, true)
	if err != nil {
		return "", err
	}
	defer func() {
		// A concurrent disappearance must not turn resume into a new staging producer.
		returnErr = errors.Join(returnErr, stage.close(stage.recovery == nil))
	}()
	if stage.recovery == nil {
		return "", errs.New(errs.CodeImageImportFailed, "retained optimization disappeared before replay")
	}
	return path, nil
}
