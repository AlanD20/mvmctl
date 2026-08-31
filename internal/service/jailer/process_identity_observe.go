package jailer

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"slices"

	"golang.org/x/sys/unix"

	"mvmctl/pkg/errs"
)

type observedProcessIdentity struct {
	process        processObservation
	mountNamespace instanceObjectIdentity
	root           instanceObjectIdentity
	executable     processExecutableDescriptorIdentity
}

type processVerificationDescriptors struct {
	deps processIdentityVerifierDeps
	vmID string
	fds  []int
}

func observeProcessIdentity(
	ctx context.Context,
	deps processIdentityVerifierDeps,
	vmID string,
	role string,
	locator instanceProcessLocator,
) (_ observedProcessIdentity, returnErr error) {
	descriptors := processVerificationDescriptors{deps: deps, vmID: vmID}
	defer func() {
		returnErr = descriptors.closeAll(ctx, returnErr)
	}()
	directoryFD, err := deps.openProcessDirectory(ctx, locator.pid)
	if err != nil {
		return observedProcessIdentity{}, processVerificationError(vmID, "open fixed "+role+" process directory", err)
	}
	descriptors.add(directoryFD)
	if err := requireProcessDescriptorCLOEXEC(ctx, deps, directoryFD, role+" process directory"); err != nil {
		return observedProcessIdentity{}, processVerificationError(vmID, "verify "+role+" process directory", err)
	}
	observation, err := observeProcessMetadata(ctx, deps, directoryFD, locator.pid)
	if err != nil {
		return observedProcessIdentity{}, processVerificationError(vmID, "read "+role+" process identity", err)
	}
	if observation.startTimeTicks != locator.startTimeTicks {
		return observedProcessIdentity{}, processVerificationError(
			vmID,
			role+" process does not match its launch locator",
			nil,
		)
	}
	mountNamespace, err := pinObservedObject(
		ctx,
		deps,
		&descriptors,
		directoryFD,
		processMountNamespaceObject,
		role+" mount namespace",
		false,
	)
	if err != nil {
		return observedProcessIdentity{}, err
	}
	root, err := pinObservedObject(
		ctx,
		deps,
		&descriptors,
		directoryFD,
		processRootObject,
		role+" root",
		true,
	)
	if err != nil {
		return observedProcessIdentity{}, err
	}
	executableFD, err := deps.pinProcessObject(ctx, directoryFD, processExecutableObject)
	if err != nil {
		return observedProcessIdentity{}, processVerificationError(vmID, "pin "+role+" executable", err)
	}
	descriptors.add(executableFD)
	if err := requireProcessDescriptorCLOEXEC(ctx, deps, executableFD, role+" executable"); err != nil {
		return observedProcessIdentity{}, processVerificationError(vmID, "verify "+role+" executable", err)
	}
	executable, err := inspectProcessExecutable(ctx, deps, executableFD)
	if err != nil {
		return observedProcessIdentity{}, processVerificationError(vmID, "inspect "+role+" executable", err)
	}
	return observedProcessIdentity{
		process: observation, mountNamespace: mountNamespace, root: root, executable: executable,
	}, nil
}

func observeProcessMetadata(
	ctx context.Context,
	deps processIdentityVerifierDeps,
	processDirectoryFD int,
	pid int,
) (processObservation, error) {
	statRaw, err := deps.readProcessMetadata(ctx, processDirectoryFD, processStatMetadata, maxProcessStatBytes)
	if err != nil {
		return processObservation{}, err
	}
	startTimeTicks, err := parseProcessStat(statRaw, pid)
	if err != nil {
		return processObservation{}, err
	}
	statusRaw, err := deps.readProcessMetadata(ctx, processDirectoryFD, processStatusMetadata, maxProcessStatusBytes)
	if err != nil {
		return processObservation{}, err
	}
	userIDs, groupIDs, groups, err := parseProcessStatus(statusRaw)
	if err != nil {
		return processObservation{}, err
	}
	cgroupRaw, err := deps.readProcessMetadata(ctx, processDirectoryFD, processCgroupMetadata, maxProcessCgroupBytes)
	if err != nil {
		return processObservation{}, err
	}
	cgroupPath, err := parseProcessCgroup(cgroupRaw)
	if err != nil {
		return processObservation{}, err
	}
	return processObservation{
		startTimeTicks: startTimeTicks,
		userIDs:        userIDs,
		groupIDs:       groupIDs,
		supplementaryGroups: append(
			[]uint32{},
			groups...,
		),
		cgroupPath: cgroupPath,
	}, nil
}

func pinObservedObject(
	ctx context.Context,
	deps processIdentityVerifierDeps,
	descriptors *processVerificationDescriptors,
	directoryFD int,
	object processPinnedObject,
	description string,
	requireDirectory bool,
) (instanceObjectIdentity, error) {
	fd, err := deps.pinProcessObject(ctx, directoryFD, object)
	if err != nil {
		return instanceObjectIdentity{}, processVerificationError(descriptors.vmID, "pin "+description, err)
	}
	descriptors.add(fd)
	if err := requireProcessDescriptorCLOEXEC(ctx, deps, fd, description); err != nil {
		return instanceObjectIdentity{}, processVerificationError(descriptors.vmID, "verify "+description, err)
	}
	identity, err := inspectProcessObject(ctx, deps, fd, requireDirectory)
	if err != nil {
		return instanceObjectIdentity{}, processVerificationError(descriptors.vmID, "inspect "+description, err)
	}
	return identity, nil
}

func inspectProcessObject(
	ctx context.Context,
	deps processIdentityVerifierDeps,
	fd int,
	requireDirectory bool,
) (instanceObjectIdentity, error) {
	var stat unix.Stat_t
	if err := deps.fstat(ctx, fd, &stat); err != nil {
		return instanceObjectIdentity{}, err
	}
	if stat.Dev == 0 || stat.Ino == 0 {
		return instanceObjectIdentity{}, fmt.Errorf("descriptor identity is invalid")
	}
	if requireDirectory && stat.Mode&unix.S_IFMT != unix.S_IFDIR {
		return instanceObjectIdentity{}, fmt.Errorf("descriptor is not a directory")
	}
	return instanceObjectIdentity{device: uint64(stat.Dev), inode: stat.Ino}, nil
}

func inspectProcessExecutable(
	ctx context.Context,
	deps processIdentityVerifierDeps,
	fd int,
) (processExecutableDescriptorIdentity, error) {
	var stat unix.Stat_t
	if err := deps.fstat(ctx, fd, &stat); err != nil {
		return processExecutableDescriptorIdentity{}, err
	}
	if stat.Dev == 0 || stat.Ino == 0 || stat.Size <= 0 || uint64(stat.Size) > maxProcessSystemExecutableBytes ||
		stat.Mode != unix.S_IFREG|0755 || stat.Uid != 0 || stat.Gid != 0 || stat.Nlink != 1 {
		return processExecutableDescriptorIdentity{}, fmt.Errorf("executable metadata does not match trusted policy")
	}
	return processExecutableDescriptorIdentity{
		device: uint64(stat.Dev), inode: stat.Ino, sizeBytes: uint64(stat.Size), mode: stat.Mode,
		uid: stat.Uid, gid: stat.Gid, links: stat.Nlink, modified: stat.Mtim, changed: stat.Ctim,
	}, nil
}

func hashProcessExecutable(
	ctx context.Context,
	deps processIdentityVerifierDeps,
	fd int,
	before processExecutableDescriptorIdentity,
) (string, processExecutableDescriptorIdentity, error) {
	digest := sha256.New()
	buffer := make([]byte, processExecutableReadBufferBytes)
	var offset int64
	for {
		if err := ctx.Err(); err != nil {
			return "", processExecutableDescriptorIdentity{}, err
		}
		count, err := deps.pread(ctx, fd, buffer, offset)
		if count < 0 || count > len(buffer) {
			return "", processExecutableDescriptorIdentity{}, fmt.Errorf("executable read count is invalid")
		}
		if count > 0 {
			_, _ = digest.Write(buffer[:count])
			offset += int64(count)
			if offset > int64(before.sizeBytes) {
				return "", processExecutableDescriptorIdentity{}, fmt.Errorf("executable grew while hashing")
			}
		}
		if err != nil && !errors.Is(err, io.EOF) {
			return "", processExecutableDescriptorIdentity{}, err
		}
		if count == 0 || errors.Is(err, io.EOF) {
			break
		}
	}
	if offset != int64(before.sizeBytes) {
		return "", processExecutableDescriptorIdentity{}, fmt.Errorf("executable size changed while hashing")
	}
	after, err := inspectProcessExecutable(ctx, deps, fd)
	if err != nil {
		return "", processExecutableDescriptorIdentity{}, err
	}
	if before != after {
		return "", processExecutableDescriptorIdentity{}, fmt.Errorf("executable metadata changed while hashing")
	}
	return fmt.Sprintf("%x", digest.Sum(nil)), after, nil
}

func requireExpectedProcessCredentials(observed processObservation, uid, gid uint32, role string) error {
	if !processCredentialIdentityMatches(uid, observed.userIDs) ||
		!processCredentialIdentityMatches(gid, observed.groupIDs) {
		return fmt.Errorf("%s credentials do not match launch authority", role)
	}
	if len(observed.supplementaryGroups) != 0 {
		return fmt.Errorf("%s supplementary groups are not empty", role)
	}
	return nil
}

func processObservationsEqual(left, right processObservation) bool {
	return left.startTimeTicks == right.startTimeTicks && left.userIDs == right.userIDs &&
		left.groupIDs == right.groupIDs && left.cgroupPath == right.cgroupPath &&
		slices.Equal(left.supplementaryGroups, right.supplementaryGroups)
}

func requireProcessDescriptorCLOEXEC(
	ctx context.Context,
	deps processIdentityVerifierDeps,
	fd int,
	description string,
) error {
	flags, err := deps.descriptorFlags(ctx, fd)
	if err != nil {
		return fmt.Errorf("inspect %s flags: %w", description, err)
	}
	if flags&unix.FD_CLOEXEC == 0 {
		return fmt.Errorf("%s does not have close-on-exec", description)
	}
	return nil
}

func (descriptors *processVerificationDescriptors) add(fd int) {
	descriptors.fds = append(descriptors.fds, fd)
}

func (descriptors *processVerificationDescriptors) closeAll(ctx context.Context, primary error) error {
	result := primary
	cleanupCtx := processCleanupContext(ctx)
	for index := len(descriptors.fds) - 1; index >= 0; index-- {
		result = appendProcessVerificationError(
			result,
			"close temporary process identity descriptor",
			descriptors.deps.close(cleanupCtx, descriptors.fds[index]),
			descriptors.vmID,
		)
	}
	descriptors.fds = nil
	return result
}

func appendProcessVerificationError(primary error, message string, cause error, vmID string) error {
	if cause == nil {
		return primary
	}
	joined := cause
	if primary != nil {
		joined = errors.Join(primary, cause)
	}
	source := errs.AsDomainError(primary)
	if source == nil {
		source = errs.AsDomainError(cause)
	}
	if source == nil {
		result := processVerificationError(vmID, message, joined)
		result.Details = map[string]any{
			"process_identity_cleanup_failed": true,
			"process_identity_cleanup_detail": boundedInstanceCleanupDetail(message, cause),
		}
		return result
	}
	cloned := *source
	cloned.Details = cloneInstanceErrorDetails(source.Details)
	cloned.Err = joined
	if cloned.Details == nil {
		cloned.Details = make(map[string]any, 2)
	}
	cloned.Details["process_identity_cleanup_failed"] = true
	if _, exists := cloned.Details["process_identity_cleanup_detail"]; !exists {
		cloned.Details["process_identity_cleanup_detail"] = boundedInstanceCleanupDetail(message, cause)
	}
	return &cloned
}

func processVerificationError(vmID, message string, cause error) *errs.DomainError {
	options := []errs.ErrorOption{errs.WithClass(errs.ClassInternal)}
	if vmID != "" {
		options = append(options, errs.WithEntity(vmID))
	}
	if cause == nil {
		return errs.New(errs.CodeProcessError, message, options...)
	}
	return errs.WrapMsg(errs.CodeProcessError, message+": "+cause.Error(), cause, options...)
}
