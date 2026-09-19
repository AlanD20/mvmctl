package image

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"

	"mvmctl/internal/lib/crypto"
	loopmountsvc "mvmctl/internal/service/loopmount"
	"mvmctl/pkg/errs"
)

const (
	stagedRecoveryLeaf     = "recovery.json"
	stagedLockLeaf         = "recovery.lock"
	maxStagedRecoveryBytes = 16 << 10
	stagedPhaseOperation   = "operation"
	stagedPhaseReady       = "ready"
	stagedPhasePublishing  = "publishing"
	stagedPhasePublished   = "published"
)

type stagedImageIdentity struct {
	Device       uint64 `json:"device"`
	Inode        uint64 `json:"inode"`
	UID          uint32 `json:"uid"`
	GID          uint32 `json:"gid"`
	Mode         uint32 `json:"mode"`
	Links        uint64 `json:"links"`
	Size         int64  `json:"size"`
	MtimeSeconds int64  `json:"mtime_seconds"`
	MtimeNanos   int64  `json:"mtime_nanos"`
	CtimeSeconds int64  `json:"ctime_seconds"`
	CtimeNanos   int64  `json:"ctime_nanos"`
}

// CRITICAL: This caller-owned record is retry intent, never root authority. Every resume
// reissues its exact typed operation; only the receiver's checked result permits publication.
type stagedImageRecovery struct {
	Version          uint32                            `json:"version"`
	SourcePath       string                            `json:"source_path"`
	SourceFilesystem loopmountsvc.Filesystem           `json:"source_filesystem"`
	Source           stagedImageIdentity               `json:"source"`
	SourceDirectory  stagedImageIdentity               `json:"source_directory"`
	StageDirectory   stagedImageIdentity               `json:"stage_directory"`
	InitialRootfs    stagedImageIdentity               `json:"initial_rootfs"`
	OSType           string                            `json:"os_type"`
	FinalPlan        loopmountsvc.ImageOptimizePlan    `json:"final_plan"`
	Request          loopmountsvc.ImageOptimizeRequest `json:"request"`
	Phase            string                            `json:"phase"`
	Result           *loopmountsvc.ImageOptimizeResult `json:"result,omitempty"`
	ResultRootfs     *stagedImageIdentity              `json:"result_rootfs,omitempty"`
	PublicationLeaf  string                            `json:"publication_leaf,omitempty"`
	Publication      *stagedImageIdentity              `json:"publication,omitempty"`
}

func stagedInfoIdentity(info os.FileInfo) (stagedImageIdentity, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return stagedImageIdentity{}, errs.New(errs.CodeImageError, "image staging metadata is unavailable")
	}
	return stagedImageIdentity{
		Device: uint64(stat.Dev), Inode: stat.Ino, UID: stat.Uid, GID: stat.Gid,
		Mode: stat.Mode, Links: uint64(stat.Nlink), Size: stat.Size,
		MtimeSeconds: stat.Mtim.Sec, MtimeNanos: stat.Mtim.Nsec,
		CtimeSeconds: stat.Ctim.Sec, CtimeNanos: stat.Ctim.Nsec,
	}, nil
}

func stagedFileIdentity(file *os.File) (stagedImageIdentity, error) {
	info, err := file.Stat()
	if err != nil {
		return stagedImageIdentity{}, errs.WrapMsg(errs.CodeImageError, "inspect retained staging file", err)
	}
	return stagedInfoIdentity(info)
}

func stagedDirectoryIdentity(root *os.Root) (stagedImageIdentity, error) {
	info, err := root.Stat(".")
	if err != nil {
		return stagedImageIdentity{}, errs.WrapMsg(errs.CodeImageError, "inspect retained staging directory", err)
	}
	return stagedInfoIdentity(info)
}

func (identity stagedImageIdentity) sameNode(other stagedImageIdentity) bool {
	return identity.Device == other.Device && identity.Inode == other.Inode &&
		identity.UID == other.UID && identity.GID == other.GID && identity.Mode == other.Mode
}

func (identity stagedImageIdentity) samePublishedFile(other stagedImageIdentity) bool {
	// A rename updates ctime. The prepared publication's inode, size, mode and mtime remain exact.
	return identity.sameNode(other) && identity.Links == other.Links && identity.Size == other.Size &&
		identity.MtimeSeconds == other.MtimeSeconds && identity.MtimeNanos == other.MtimeNanos
}

func (identity stagedImageIdentity) callerFile() bool {
	return identity.Mode&unix.S_IFMT == unix.S_IFREG && identity.Links == 1 &&
		identity.UID == uint32(os.Geteuid()) && identity.Mode&07777 == 0600
}

func (stage *stagedImage) lockStaging(ctx context.Context, create bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	flags := os.O_RDWR | unix.O_NOFOLLOW | unix.O_NONBLOCK
	if create {
		flags |= os.O_CREATE | os.O_EXCL
	}
	file, err := stage.root.OpenFile(stagedLockLeaf, flags, 0600)
	if err != nil {
		return errs.WrapMsg(errs.CodeImageError, "open image recovery lock", err)
	}
	stage.lock = file
	identity, err := stagedFileIdentity(file)
	if err != nil || !identity.callerFile() || identity.Size != 0 {
		return errs.WrapMsg(errs.CodeImageError, "image recovery lock has unsafe metadata", err)
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return errs.WrapMsg(errs.CodeImageError, "another image staging operation owns the recovery lock", err)
	}
	stage.lockMade = create
	return nil
}

func (stage *stagedImage) prepareRecovery(ctx context.Context, osType string, deblob, shrink bool) error {
	plan := loopmountsvc.ImageOptimizePlan{Shrink: shrink}
	if deblob {
		plan.DeblobOS = &osType
	}
	if stage.recovery != nil {
		if stage.recovery.OSType != osType || !sameStagedPlan(stage.recovery.FinalPlan, plan) {
			return errs.New(errs.CodeImageError, "retained image optimization intent differs from this request")
		}
		return nil
	}
	source, err := stagedInfoIdentity(stage.sourceInfo)
	if err != nil {
		return err
	}
	sourceDirectory, err := stagedDirectoryIdentity(stage.publication)
	if err != nil {
		return err
	}
	stageDirectory, err := stagedDirectoryIdentity(stage.root)
	if err != nil {
		return err
	}
	request := loopmountsvc.ImageOptimizeRequest{
		CacheLocator: stage.request.CacheLocator, ImageID: stage.request.ImageID,
		ExpectedImageBytes: stage.request.ExpectedImageBytes, Filesystem: stage.request.Filesystem, Plan: plan,
	}
	if request.Filesystem == loopmountsvc.FilesystemBtrfs {
		ext4 := loopmountsvc.FilesystemExt4
		request.Plan = loopmountsvc.ImageOptimizePlan{ConvertTo: &ext4}
	}
	stage.recovery = &stagedImageRecovery{
		Version: 1, SourcePath: stage.sourcePath, SourceFilesystem: stage.request.Filesystem,
		Source: source, SourceDirectory: sourceDirectory, StageDirectory: stageDirectory,
		InitialRootfs: stage.rootfsIdentity, OSType: osType, FinalPlan: plan,
		Request: request, Phase: stagedPhaseOperation,
	}
	return stage.saveRecovery(ctx)
}

func sameStagedPlan(left, right loopmountsvc.ImageOptimizePlan) bool {
	if left.Shrink != right.Shrink || left.FixFstab != right.FixFstab ||
		(left.ConvertTo == nil) != (right.ConvertTo == nil) || (left.DeblobOS == nil) != (right.DeblobOS == nil) {
		return false
	}
	return (left.ConvertTo == nil || *left.ConvertTo == *right.ConvertTo) &&
		(left.DeblobOS == nil || *left.DeblobOS == *right.DeblobOS)
}

func (stage *stagedImage) loadRecovery(ctx context.Context) (returnErr error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	file, err := stage.root.OpenFile(stagedRecoveryLeaf, os.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return errs.WrapMsg(errs.CodeImageError, "retained image staging has no readable recovery record", err)
	}
	defer func() { returnErr = errors.Join(returnErr, file.Close()) }()
	identity, err := stagedFileIdentity(file)
	if err != nil || !identity.callerFile() || identity.Size <= 0 || identity.Size > maxStagedRecoveryBytes {
		return errs.WrapMsg(errs.CodeImageError, "image recovery record has unsafe metadata", err)
	}
	data, err := io.ReadAll(io.LimitReader(file, maxStagedRecoveryBytes+1))
	if err != nil || len(data) > maxStagedRecoveryBytes {
		return errs.WrapMsg(errs.CodeImageError, "read bounded image recovery record", err)
	}
	var record stagedImageRecovery
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&record); err != nil {
		return errs.WrapMsg(errs.CodeImageError, "decode image recovery record", err)
	}
	canonical, err := json.Marshal(record)
	if err != nil || !bytes.Equal(data, append(canonical, '\n')) {
		return errs.WrapMsg(errs.CodeImageError, "image recovery record is not canonical", err)
	}
	after, err := stagedFileIdentity(file)
	if err != nil || after != identity {
		return errs.WrapMsg(errs.CodeImageError, "image recovery record changed while reading", err)
	}
	if err := stage.admitRecovery(record); err != nil {
		return err
	}
	stage.recovery = &record
	stage.manifestIdentity = identity
	stage.manifestMade = true
	stage.lockMade = true
	stage.directoryMade = true
	stage.request.Filesystem = record.SourceFilesystem
	stage.request.ExpectedImageBytes = uint64(record.Source.Size)
	return nil
}

func (stage *stagedImage) admitRecovery(record stagedImageRecovery) error {
	if record.Version != 1 || record.SourcePath != stage.sourcePath ||
		record.Request.CacheLocator != stage.request.CacheLocator || record.Request.ImageID != stage.request.ImageID {
		return errs.New(errs.CodeImageError, "image recovery identity does not match this staging request")
	}
	if err := record.Request.Validate(); err != nil {
		return err
	}
	if err := record.SourceFilesystem.Validate(); err != nil {
		return err
	}
	hint := loopmountsvc.ImageInspectResult{ImageID: record.Request.ImageID, OSType: record.OSType}
	if err := hint.Validate(stage.request); err != nil {
		return err
	}
	if record.Source.Size <= 0 || record.Source.Mode&unix.S_IFMT != unix.S_IFREG ||
		!record.InitialRootfs.callerFile() || record.InitialRootfs.Size != record.Source.Size ||
		record.FinalPlan.ConvertTo != nil || record.FinalPlan.FixFstab {
		return errs.New(errs.CodeImageError, "image recovery source or final intent is invalid")
	}
	source, err := stagedInfoIdentity(stage.sourceInfo)
	if err != nil {
		return err
	}
	published := (record.Phase == stagedPhasePublishing || record.Phase == stagedPhasePublished) &&
		record.Publication != nil && record.Publication.samePublishedFile(source)
	if source != record.Source && !published {
		return errs.New(errs.CodeImageError, "original image identity changed since optimization began")
	}
	if !published && stage.request.Filesystem != record.SourceFilesystem {
		return errs.New(errs.CodeImageError, "original image filesystem differs from the recovery request")
	}
	directory, err := stagedDirectoryIdentity(stage.root)
	if err != nil {
		return err
	}
	sourceDirectory, err := stagedDirectoryIdentity(stage.publication)
	if err != nil {
		return err
	}
	if !record.StageDirectory.sameNode(directory) || directory.Mode&07777 != 0700 ||
		directory.UID != uint32(os.Geteuid()) || !record.SourceDirectory.sameNode(sourceDirectory) {
		return errs.New(errs.CodeImageError, "image recovery directory identity changed")
	}
	if record.Request.Plan.ConvertTo != nil {
		if record.SourceFilesystem != loopmountsvc.FilesystemBtrfs ||
			record.Request.Filesystem != loopmountsvc.FilesystemBtrfs ||
			*record.Request.Plan.ConvertTo != loopmountsvc.FilesystemExt4 || record.Request.Plan.Shrink ||
			record.Request.Plan.FixFstab || record.Request.Plan.DeblobOS != nil ||
			record.Request.ExpectedImageBytes != uint64(record.Source.Size) {
			return errs.New(errs.CodeImageError, "image recovery conversion intent is invalid")
		}
	} else if !sameStagedPlan(record.Request.Plan, record.FinalPlan) ||
		(record.SourceFilesystem != loopmountsvc.FilesystemBtrfs &&
			(record.Request.Filesystem != record.SourceFilesystem ||
				record.Request.ExpectedImageBytes != uint64(record.Source.Size))) ||
		(record.SourceFilesystem == loopmountsvc.FilesystemBtrfs && record.Request.Filesystem != loopmountsvc.FilesystemExt4) {
		return errs.New(errs.CodeImageError, "image recovery optimization intent is invalid")
	}
	switch record.Phase {
	case stagedPhaseOperation:
		if record.Result != nil || record.ResultRootfs != nil || record.Publication != nil || record.PublicationLeaf != "" {
			return errs.New(errs.CodeImageError, "pending image recovery record contains completion state")
		}
	case stagedPhaseReady, stagedPhasePublishing, stagedPhasePublished:
		if record.Result == nil || record.ResultRootfs == nil || !record.ResultRootfs.callerFile() ||
			uint64(record.ResultRootfs.Size) != record.Result.ImageBytes {
			return errs.New(errs.CodeImageError, "completed image recovery record lacks result identity")
		}
		if err := record.Result.Validate(record.Request); err != nil {
			return err
		}
		if record.Phase == stagedPhaseReady && (record.Publication != nil || record.PublicationLeaf != "") {
			return errs.New(errs.CodeImageError, "unpublished image recovery record contains publication identity")
		}
		if record.Phase != stagedPhaseReady && (record.Publication == nil || !record.Publication.callerFile() ||
			!validStagedPublicationLeaf(record.PublicationLeaf) ||
			record.Publication.Size != record.ResultRootfs.Size) {
			return errs.New(errs.CodeImageError, "image publication recovery identity is invalid")
		}
	default:
		return errs.New(errs.CodeImageError, "image recovery phase is invalid")
	}
	return nil
}

func (stage *stagedImage) saveRecovery(ctx context.Context) (returnErr error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	data, err := json.Marshal(stage.recovery)
	if err != nil || len(data)+1 > maxStagedRecoveryBytes {
		return errs.WrapMsg(errs.CodeImageError, "encode bounded image recovery record", err)
	}
	temporary := ".recovery-" + crypto.UUIDV4() + ".tmp"
	file, err := stage.root.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_RDWR|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return errs.WrapMsg(errs.CodeImageError, "create image recovery record", err)
	}
	initial, initialErr := stagedFileIdentity(file)
	defer func() {
		if temporary != "" {
			if initialErr == nil {
				returnErr = errors.Join(returnErr, stage.removeKnownLeaf(context.WithoutCancel(ctx), temporary, initial))
			}
		}
	}()
	if initialErr != nil {
		return errors.Join(initialErr, file.Close())
	}
	_, writeErr := file.Write(append(data, '\n'))
	syncErr := file.Sync()
	identity, statErr := stagedFileIdentity(file)
	if err := errors.Join(writeErr, syncErr, statErr, file.Close()); err != nil {
		return errs.WrapMsg(errs.CodeImageError, "persist image recovery record", err)
	}
	if stage.manifestMade {
		if err := stage.requireKnownLeaf(stagedRecoveryLeaf, stage.manifestIdentity); err != nil {
			return err
		}
	} else if _, err := stage.root.Lstat(stagedRecoveryLeaf); !errors.Is(err, os.ErrNotExist) {
		return errs.WrapMsg(errs.CodeImageError, "image recovery record already exists", err)
	}
	if err := stage.root.Rename(temporary, stagedRecoveryLeaf); err != nil {
		return errs.WrapMsg(errs.CodeImageError, "publish image recovery record", err)
	}
	temporary = ""
	stage.manifestMade = true
	stage.manifestIdentity = identity
	return syncStagedDirectory(ctx, stage.root)
}

func (stage *stagedImage) resumeOptimization(ctx context.Context) error {
	record := stage.recovery
	if record.Phase == stagedPhasePublished {
		_, err := stage.root.Lstat("rootfs.img")
		if errors.Is(err, os.ErrNotExist) {
			current, err := stage.publicationIdentity(filepath.Base(stage.sourcePath))
			if err != nil || !record.Publication.samePublishedFile(current) {
				return errs.WrapMsg(errs.CodeImageError, "published image identity changed before caller cleanup", err)
			}
			// No image is published or removed in this terminal case. Only this caller's exact
			// manifest and lock remain; their removal conveys no authority over root's receipts.
			stage.callerCleanupOnly = true
			return nil
		}
		if err != nil {
			return errs.WrapMsg(errs.CodeImageError, "inspect published staging cleanup state", err)
		}
	}
	for {
		// Even a saved completed phase must recover through the root-owned receipt. A caller
		// cannot turn a fabricated result or manifest into permission to publish an image.
		stage.cleanupKnown = false
		result, err := loopmountsvc.NewClient().OptimizeImage(ctx, record.Request)
		if err != nil {
			return err
		}
		if err := result.Validate(record.Request); err != nil {
			return err
		}
		stage.cleanupKnown = true
		if record.Request.Plan.ConvertTo != nil && (record.FinalPlan.Shrink || record.FinalPlan.DeblobOS != nil) {
			record.Request = loopmountsvc.ImageOptimizeRequest{
				CacheLocator: record.Request.CacheLocator, ImageID: record.Request.ImageID,
				ExpectedImageBytes: result.ImageBytes, Filesystem: result.Filesystem, Plan: record.FinalPlan,
			}
			record.Phase = stagedPhaseOperation
			record.Result = nil
			record.ResultRootfs = nil
			if err := stage.saveRecovery(ctx); err != nil {
				return err
			}
			continue
		}
		candidate, err := stage.root.OpenFile("rootfs.img", os.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
		if err != nil {
			return errs.WrapMsg(errs.CodeImageError, "open completed image optimization candidate", err)
		}
		identity, statErr := stagedFileIdentity(candidate)
		if err := errors.Join(statErr, candidate.Close()); err != nil {
			return err
		}
		if !identity.callerFile() || uint64(identity.Size) != result.ImageBytes {
			return errs.New(errs.CodeImageError, "completed staging candidate has unsafe metadata")
		}
		if record.Result != nil && (*record.Result != result || record.ResultRootfs == nil ||
			!record.ResultRootfs.samePublishedFile(identity)) {
			return errs.New(errs.CodeImageError, "replayed image optimization result differs from retained completion")
		}
		stage.rootfsIdentity = identity
		stage.rootfsMade = true
		record.Result = &result
		record.ResultRootfs = &identity
		if record.Phase == stagedPhaseOperation {
			record.Phase = stagedPhaseReady
			if err := stage.saveRecovery(ctx); err != nil {
				return err
			}
		}
		return stage.publishRecoveredImage(ctx)
	}
}

func syncStagedDirectory(ctx context.Context, root *os.Root) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	directory, err := root.Open(".")
	if err != nil {
		return errs.WrapMsg(errs.CodeImageError, "open staging directory for sync", err)
	}
	return errors.Join(directory.Sync(), directory.Close())
}

func (stage *stagedImage) requireKnownLeaf(name string, identity stagedImageIdentity) error {
	info, err := stage.root.Lstat(name)
	if err != nil {
		return errs.WrapMsg(errs.CodeImageError, "inspect owned staging leaf", err)
	}
	current, err := stagedInfoIdentity(info)
	if err != nil || !identity.sameNode(current) || current.Links != 1 {
		return errs.WrapMsg(errs.CodeImageError, "owned staging leaf identity changed", err)
	}
	return nil
}

func (stage *stagedImage) removeKnownLeaf(ctx context.Context, name string, identity stagedImageIdentity) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := stage.requireKnownLeaf(name, identity); err != nil {
		return err
	}
	return stage.root.Remove(name)
}

func (stage *stagedImage) removeRecoveryManifest(ctx context.Context) error {
	return stage.removeKnownLeaf(ctx, stagedRecoveryLeaf, stage.manifestIdentity)
}

func (stage *stagedImage) removeLock(ctx context.Context) error {
	identity, err := stagedFileIdentity(stage.lock)
	if err != nil {
		return err
	}
	return stage.removeKnownLeaf(ctx, stagedLockLeaf, identity)
}

func (stage *stagedImage) removeKnownDirectory(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	info, err := stage.parent.Lstat(stage.request.ImageID)
	if err != nil {
		return errs.WrapMsg(errs.CodeImageError, "inspect owned staging directory before cleanup", err)
	}
	identity, err := stagedInfoIdentity(info)
	if err != nil || !stage.directoryIdentity.sameNode(identity) {
		return errs.WrapMsg(errs.CodeImageError, "owned staging directory identity changed before cleanup", err)
	}
	if err := stage.parent.Remove(stage.request.ImageID); err != nil {
		return errs.WrapMsg(errs.CodeImageError, "remove completed owned staging directory", err)
	}
	return syncStagedDirectory(ctx, stage.parent)
}

func validStagedPublicationLeaf(name string) bool {
	return strings.HasPrefix(name, ".mvm-image-") && len(name) == len(".mvm-image-")+36 &&
		!strings.ContainsAny(name, "/\\\x00") && filepath.Base(name) == name
}
