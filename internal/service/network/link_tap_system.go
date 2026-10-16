package network

import (
	"context"
	"errors"
	"fmt"

	"golang.org/x/sys/unix"

	"mvmctl/pkg/errs"
)

const (
	tapDevicePath  = "/dev/net/tun"
	tapDeviceMajor = uint32(10)
	tapDeviceMinor = uint32(200)
	tapDeviceFlags = unix.O_RDWR | unix.O_CLOEXEC | unix.O_NOFOLLOW
	tapIFFFlags    = uint16(unix.IFF_TAP | unix.IFF_NO_PI | unix.IFF_TUN_EXCL)
)

type tapEffectDeps struct {
	namespace namespaceEffectDeps

	openDevice    func(context.Context, string, int, uint32) (int, error)
	fstatDevice   func(context.Context, int, *unix.Stat_t) error
	closeDevice   func(context.Context, int) error
	setIFF        func(context.Context, int, linkName, uint16) error
	setOwner      func(context.Context, int, OwnerUID) error
	setPersistent func(context.Context, int, bool) error
}

type tapEffect struct {
	deps   tapEffectDeps
	policy namespaceEffectPolicy
	route  routeNetlink
}

func newTAPEffect(namespace *namespaceEffect, route routeNetlink) *tapEffect {
	if namespace == nil {
		return &tapEffect{}
	}
	namespaceDeps := namespace.deps
	return &tapEffect{
		deps: tapEffectDeps{
			namespace:   namespaceDeps,
			openDevice:  namespaceDeps.open,
			fstatDevice: namespaceDeps.fstat,
			closeDevice: namespaceDeps.close,
			setIFF: func(ctx context.Context, fd int, name linkName, flags uint16) error {
				if err := namespaceEffectContextErr(ctx); err != nil {
					return err
				}
				request, err := unix.NewIfreq(name.String())
				if err != nil {
					return fmt.Errorf("build TUNSETIFF request: %w", err)
				}
				request.SetUint16(flags)
				if err := unix.IoctlIfreq(fd, unix.TUNSETIFF, request); err != nil {
					return err
				}
				if request.Name() != name.String() {
					return errors.New("TUNSETIFF returned a different interface name")
				}
				return nil
			},
			setOwner: func(ctx context.Context, fd int, owner OwnerUID) error {
				if err := namespaceEffectContextErr(ctx); err != nil {
					return err
				}
				return unix.IoctlSetInt(fd, unix.TUNSETOWNER, int(owner))
			},
			setPersistent: func(ctx context.Context, fd int, persistent bool) error {
				if err := namespaceEffectContextErr(ctx); err != nil {
					return err
				}
				value := 0
				if persistent {
					value = 1
				}
				return unix.IoctlSetInt(fd, unix.TUNSETPERSIST, value)
			},
		},
		policy: namespace.policy,
		route:  route,
	}
}

func validateTAPEffectDeps(deps tapEffectDeps) error {
	if validateNamespaceEffectDeps(deps.namespace) != nil || deps.openDevice == nil || deps.fstatDevice == nil ||
		deps.closeDevice == nil || deps.setIFF == nil || deps.setOwner == nil || deps.setPersistent == nil {
		return errors.New("TAP effect dependencies are incomplete")
	}
	return nil
}

func (effect *tapEffect) createTAPDown(
	ctx context.Context,
	targetFD int,
	targetIdentity namespaceIdentity,
	name linkName,
	alias string,
	owner OwnerUID,
) error {
	if ctx == nil {
		return errors.New("TAP creation context is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if effect == nil || effect.route == nil || targetFD <= 0 || !validNamespaceIdentity(targetIdentity) || owner == 0 ||
		validateTAPEffectDeps(effect.deps) != nil {
		return errors.New("TAP creation authority is incomplete")
	}
	return effect.deps.namespace.runWorker(ctx, func(workerCtx context.Context) error {
		return effect.createTAPDownOnWorker(workerCtx, targetFD, targetIdentity, name, alias, owner)
	})
}

func (effect *tapEffect) createTAPDownOnWorker(
	ctx context.Context,
	targetFD int,
	targetIdentity namespaceIdentity,
	name linkName,
	alias string,
	owner OwnerUID,
) (returnErr error) {
	namespaceDeps := effect.deps.namespace
	threadLocked := false
	mustRestore := false
	hostFD := -1
	deviceFD := -1
	hostIdentity := namespaceIdentity{}
	defer func() {
		cleanupCtx := context.WithoutCancel(ctx)
		restoreSafe := !mustRestore
		var restoreErr error
		if mustRestore {
			restoreSafe, restoreErr = restoreHostNetworkNamespace(
				cleanupCtx, namespaceDeps, effect.policy, hostFD, hostIdentity,
			)
		}
		deviceCloseErr := closeConsumedTAPDevice(cleanupCtx, effect.deps, &deviceFD)
		hostCloseErr := closeConsumedNamespaceFD(cleanupCtx, namespaceDeps, &hostFD)
		var unlockErr error
		if threadLocked && restoreSafe {
			unlockErr = namespaceDeps.unlockThread(cleanupCtx)
		}
		if mustRestore && !restoreSafe {
			returnErr = markTAPThreadFatal(
				name,
				errors.Join(returnErr, deviceCloseErr, hostCloseErr, unlockErr),
				restoreErr,
			)
			return
		}
		returnErr = errors.Join(returnErr, restoreErr, deviceCloseErr, hostCloseErr, unlockErr)
	}()

	if err := namespaceDeps.lockThread(ctx); err != nil {
		return fmt.Errorf("lock TAP namespace worker: %w", err)
	}
	threadLocked = true
	hostFD, returnErr = namespaceDeps.open(ctx, namespaceThreadNetworkPath, namespaceCurrentOpenFlags, 0)
	if returnErr != nil {
		return fmt.Errorf("pin host namespace for TAP creation: %w", returnErr)
	}
	hostIdentity, returnErr = inspectNetworkNamespaceFD(ctx, namespaceDeps, effect.policy, hostFD)
	if returnErr != nil {
		return fmt.Errorf("inspect host namespace for TAP creation: %w", returnErr)
	}
	targetBefore, err := inspectNetworkNamespaceFD(ctx, namespaceDeps, effect.policy, targetFD)
	if err != nil {
		return fmt.Errorf("inspect target TAP namespace: %w", err)
	}
	if targetBefore != targetIdentity {
		return errors.New("target TAP namespace identity changed")
	}
	mustRestore = true
	if err := namespaceDeps.setns(ctx, targetFD, unix.CLONE_NEWNET); err != nil {
		return fmt.Errorf("enter target TAP namespace: %w", err)
	}
	currentIdentity, err := inspectCurrentNetworkNamespace(ctx, namespaceDeps, effect.policy)
	if err != nil {
		return fmt.Errorf("verify target TAP namespace: %w", err)
	}
	if currentIdentity != targetIdentity {
		return errors.New("TAP worker entered the wrong network namespace")
	}
	deviceFD, err = effect.deps.openDevice(ctx, tapDevicePath, tapDeviceFlags, 0)
	if err != nil {
		return fmt.Errorf("open fixed TUN device: %w", err)
	}
	if err := inspectTAPDevice(ctx, effect.deps, deviceFD); err != nil {
		return err
	}
	if err := effect.deps.setIFF(ctx, deviceFD, name, tapIFFFlags); err != nil {
		return fmt.Errorf("create exact TAP queue: %w", err)
	}
	if err := effect.deps.setOwner(ctx, deviceFD, owner); err != nil {
		return fmt.Errorf("set authenticated TAP owner: %w", err)
	}
	created, err := effect.route.inspectLink(ctx, targetFD, name)
	if err != nil {
		return fmt.Errorf("inspect temporary TAP: %w", err)
	}
	if created == nil {
		return errors.New("created temporary TAP is absent")
	}
	identity := linkIdentity{name: name, ifIndex: created.ifIndex, namespace: targetIdentity}
	if err := errors.Join(
		validateManagedLinkBase(*created, identity, linkKindTAP, "", 0),
		validateManagedTAPAuthority(*created, owner, 0),
	); err != nil {
		return fmt.Errorf("admit temporary TAP: %w", err)
	}
	if err := effect.route.setLinkAlias(ctx, targetFD, created.ifIndex, alias); err != nil {
		return fmt.Errorf("set managed TAP alias: %w", err)
	}
	aliased, err := effect.route.inspectLink(ctx, targetFD, name)
	if err != nil {
		return fmt.Errorf("inspect aliased temporary TAP: %w", err)
	}
	if aliased == nil {
		return errors.New("aliased temporary TAP is absent")
	}
	if err := errors.Join(
		validateManagedLinkBase(*aliased, identity, linkKindTAP, alias, 0),
		validateManagedTAPAuthority(*aliased, owner, 0),
	); err != nil {
		return fmt.Errorf("admit aliased temporary TAP: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := effect.deps.setPersistent(ctx, deviceFD, true); err != nil {
		return fmt.Errorf("make TAP persistent: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}

func inspectCurrentNetworkNamespace(
	ctx context.Context,
	deps namespaceEffectDeps,
	policy namespaceEffectPolicy,
) (_ namespaceIdentity, returnErr error) {
	currentFD, err := deps.open(ctx, namespaceThreadNetworkPath, namespaceCurrentOpenFlags, 0)
	if err != nil {
		return namespaceIdentity{}, err
	}
	defer func() {
		returnErr = errors.Join(returnErr, closeConsumedNamespaceFD(context.WithoutCancel(ctx), deps, &currentFD))
	}()
	return inspectNetworkNamespaceFD(ctx, deps, policy, currentFD)
}

func inspectTAPDevice(ctx context.Context, deps tapEffectDeps, fd int) error {
	var stat unix.Stat_t
	if err := deps.fstatDevice(ctx, fd, &stat); err != nil {
		return fmt.Errorf("inspect fixed TUN device: %w", err)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFCHR || unix.Major(uint64(stat.Rdev)) != tapDeviceMajor ||
		unix.Minor(uint64(stat.Rdev)) != tapDeviceMinor {
		return errors.New("fixed TUN device has unexpected type or device identity")
	}
	return nil
}

func closeConsumedTAPDevice(ctx context.Context, deps tapEffectDeps, descriptor *int) error {
	if descriptor == nil || *descriptor < 0 {
		return nil
	}
	fd := *descriptor
	*descriptor = -1
	if err := deps.closeDevice(context.WithoutCancel(ctx), fd); err != nil {
		return fmt.Errorf("close TAP queue descriptor: %w", err)
	}
	return nil
}

func markTAPThreadFatal(name linkName, primary, cause error) *errs.DomainError {
	return errs.WrapMsg(
		errs.CodeNetworkCreateFailed,
		"network namespace thread restoration failed",
		errors.Join(primary, cause),
		errs.WithClass(errs.ClassInternal),
		errs.WithEntity(name.String()),
		errs.WithDetails(map[string]any{
			"network_worker_retired":      true,
			"network_thread_contaminated": true,
		}),
	)
}
