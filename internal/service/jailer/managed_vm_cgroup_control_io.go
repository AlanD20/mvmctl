package jailer

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

const managedVMCgroupControlMaxBytes = 4096

type managedVMCgroupControlFile uint8

const (
	managedVMCgroupControllersControl managedVMCgroupControlFile = iota + 1
	managedVMCgroupSubtreeControl
	managedVMCgroupCPUMaxControl
	managedVMCgroupCPUWeightControl
	managedVMCgroupMemoryHighControl
	managedVMCgroupMemoryMaxControl
	managedVMCgroupMemorySwapMaxControl
	managedVMCgroupPIDsMaxControl
)

type managedVMCgroupControlFileSpec struct {
	name     string
	mode     uint32
	writable bool
}

type managedVMCgroupControllerSet uint8

const (
	managedVMCgroupControllerCPU managedVMCgroupControllerSet = 1 << iota
	managedVMCgroupControllerMemory
	managedVMCgroupControllerPIDs
	managedVMCgroupKnownControllers = managedVMCgroupControllerCPU |
		managedVMCgroupControllerMemory |
		managedVMCgroupControllerPIDs
	managedVMCgroupRequiredControllers = managedVMCgroupKnownControllers
)

type managedVMCgroupCPUMax struct {
	quotaMicros  uint64
	periodMicros uint64
}

func readManagedVMCgroupCPUMax(
	ctx context.Context,
	deps managedVMCgroupDeps,
	directoryFD int,
	directory managedVMCgroupIdentity,
) (managedVMCgroupCPUMax, error) {
	raw, err := readManagedVMCgroupControlBytes(
		ctx,
		deps,
		directoryFD,
		directory,
		managedVMCgroupCPUMaxControl,
	)
	if err != nil {
		return managedVMCgroupCPUMax{}, err
	}
	value, err := parseManagedVMCgroupCPUMax(raw)
	if err != nil {
		return managedVMCgroupCPUMax{}, instanceAtomicError("parse cpu.max control", err)
	}
	return value, nil
}

func readManagedVMCgroupCPUWeight(
	ctx context.Context,
	deps managedVMCgroupDeps,
	directoryFD int,
	directory managedVMCgroupIdentity,
) (uint64, error) {
	return readManagedVMCgroupFiniteControl(
		ctx,
		deps,
		directoryFD,
		directory,
		managedVMCgroupCPUWeightControl,
		"cpu.weight",
	)
}

func readManagedVMCgroupMemoryHigh(
	ctx context.Context,
	deps managedVMCgroupDeps,
	directoryFD int,
	directory managedVMCgroupIdentity,
) (uint64, error) {
	return readManagedVMCgroupFiniteControl(
		ctx,
		deps,
		directoryFD,
		directory,
		managedVMCgroupMemoryHighControl,
		"memory.high",
	)
}

func readManagedVMCgroupMemoryMax(
	ctx context.Context,
	deps managedVMCgroupDeps,
	directoryFD int,
	directory managedVMCgroupIdentity,
) (uint64, error) {
	return readManagedVMCgroupFiniteControl(
		ctx,
		deps,
		directoryFD,
		directory,
		managedVMCgroupMemoryMaxControl,
		"memory.max",
	)
}

func readManagedVMCgroupMemorySwapMax(
	ctx context.Context,
	deps managedVMCgroupDeps,
	directoryFD int,
	directory managedVMCgroupIdentity,
) (uint64, error) {
	return readManagedVMCgroupFiniteControl(
		ctx,
		deps,
		directoryFD,
		directory,
		managedVMCgroupMemorySwapMaxControl,
		"memory.swap.max",
	)
}

func readManagedVMCgroupPIDsMax(
	ctx context.Context,
	deps managedVMCgroupDeps,
	directoryFD int,
	directory managedVMCgroupIdentity,
) (uint64, error) {
	return readManagedVMCgroupFiniteControl(
		ctx,
		deps,
		directoryFD,
		directory,
		managedVMCgroupPIDsMaxControl,
		"pids.max",
	)
}

func writeManagedVMCgroupCPUMax(
	ctx context.Context,
	deps managedVMCgroupDeps,
	directoryFD int,
	directory managedVMCgroupIdentity,
	value managedVMCgroupCPUMax,
) error {
	raw, err := formatManagedVMCgroupCPUMax(value)
	if err != nil {
		return instanceAtomicError("format cpu.max control", err)
	}
	return writeManagedVMCgroupControlBytes(
		ctx,
		deps,
		directoryFD,
		directory,
		managedVMCgroupCPUMaxControl,
		raw,
	)
}

func readManagedVMCgroupFiniteControl(
	ctx context.Context,
	deps managedVMCgroupDeps,
	directoryFD int,
	directory managedVMCgroupIdentity,
	control managedVMCgroupControlFile,
	description string,
) (uint64, error) {
	raw, err := readManagedVMCgroupControlBytes(ctx, deps, directoryFD, directory, control)
	if err != nil {
		return 0, err
	}
	value, err := parseManagedVMCgroupFiniteUint(raw)
	if err != nil {
		return 0, instanceAtomicError("parse "+description+" control", err)
	}
	return value, nil
}

func readManagedVMCgroupControllers(
	ctx context.Context,
	deps managedVMCgroupDeps,
	directoryFD int,
	directory managedVMCgroupIdentity,
) (managedVMCgroupControllerSet, error) {
	raw, err := readManagedVMCgroupControlBytes(
		ctx,
		deps,
		directoryFD,
		directory,
		managedVMCgroupControllersControl,
	)
	if err != nil {
		return 0, err
	}
	controllers, err := parseManagedVMCgroupControllerSet(raw)
	if err != nil {
		return 0, instanceAtomicError("parse cgroup.controllers control", err)
	}
	return controllers, nil
}

func readManagedVMCgroupSubtreeControl(
	ctx context.Context,
	deps managedVMCgroupDeps,
	directoryFD int,
	directory managedVMCgroupIdentity,
) (managedVMCgroupControllerSet, error) {
	raw, err := readManagedVMCgroupControlBytes(
		ctx,
		deps,
		directoryFD,
		directory,
		managedVMCgroupSubtreeControl,
	)
	if err != nil {
		return 0, err
	}
	controllers, err := parseManagedVMCgroupControllerSet(raw)
	if err != nil {
		return 0, instanceAtomicError("parse cgroup.subtree_control control", err)
	}
	return controllers, nil
}

func addManagedVMCgroupSubtreeControllers(
	ctx context.Context,
	deps managedVMCgroupDeps,
	directoryFD int,
	directory managedVMCgroupIdentity,
	controllers managedVMCgroupControllerSet,
) error {
	raw, err := formatManagedVMCgroupControllerEnablement(controllers)
	if err != nil {
		return instanceAtomicError("format cgroup.subtree_control enablement", err)
	}
	return writeManagedVMCgroupControlBytes(
		ctx,
		deps,
		directoryFD,
		directory,
		managedVMCgroupSubtreeControl,
		raw,
	)
}

func ensureManagedVMCgroupControllersReady(
	ctx context.Context,
	deps managedVMCgroupDeps,
	directoryFD int,
	directory managedVMCgroupIdentity,
) error {
	available, err := readManagedVMCgroupControllers(ctx, deps, directoryFD, directory)
	if err != nil {
		return err
	}
	if err := requireManagedVMCgroupControllers(available, managedVMCgroupRequiredControllers); err != nil {
		return instanceAtomicError("required cgroup-v2 controller is unavailable", err)
	}
	enabled, err := readManagedVMCgroupSubtreeControl(ctx, deps, directoryFD, directory)
	if err != nil {
		return err
	}
	missing := managedVMCgroupRequiredControllers &^ enabled
	if missing != 0 {
		if err := addManagedVMCgroupSubtreeControllers(ctx, deps, directoryFD, directory, missing); err != nil {
			return err
		}
	}
	enabled, err = readManagedVMCgroupSubtreeControl(ctx, deps, directoryFD, directory)
	if err != nil {
		return err
	}
	if err := requireManagedVMCgroupControllers(enabled, managedVMCgroupRequiredControllers); err != nil {
		return instanceAtomicError("required cgroup-v2 controller enablement is incomplete", err)
	}
	return nil
}

func readManagedVMCgroupControlBytes(
	ctx context.Context,
	deps managedVMCgroupDeps,
	directoryFD int,
	directory managedVMCgroupIdentity,
	control managedVMCgroupControlFile,
) (_ []byte, returnErr error) {
	if ctx == nil {
		return nil, instanceAtomicError("read managed VM cgroup control: context is required", nil)
	}
	if err := ctx.Err(); err != nil {
		return nil, instanceAtomicError("read managed VM cgroup control", err)
	}
	if deps.pread == nil {
		return nil, instanceAtomicError("managed VM cgroup control read dependency is incomplete", nil)
	}
	fd, spec, err := openManagedVMCgroupControl(ctx, deps, directoryFD, directory, control, false)
	if err != nil {
		return nil, err
	}
	defer func() {
		returnErr = appendInstanceOperationError(
			returnErr,
			"close managed VM cgroup control after read",
			deps.close(context.WithoutCancel(ctx), fd),
		)
	}()
	return readAllManagedVMCgroupControl(ctx, deps, fd, spec.name)
}

func writeManagedVMCgroupControlBytes(
	ctx context.Context,
	deps managedVMCgroupDeps,
	directoryFD int,
	directory managedVMCgroupIdentity,
	control managedVMCgroupControlFile,
	value []byte,
) (returnErr error) {
	if ctx == nil {
		return instanceAtomicError("write managed VM cgroup control: context is required", nil)
	}
	if err := ctx.Err(); err != nil {
		return instanceAtomicError("write managed VM cgroup control", err)
	}
	if len(value) == 0 || len(value) > managedVMCgroupControlMaxBytes {
		return instanceAtomicError("managed VM cgroup control write size is invalid", nil)
	}
	if deps.write == nil {
		return instanceAtomicError("managed VM cgroup control write dependency is incomplete", nil)
	}
	fd, spec, err := openManagedVMCgroupControl(ctx, deps, directoryFD, directory, control, true)
	if err != nil {
		return err
	}
	defer func() {
		returnErr = appendInstanceOperationError(
			returnErr,
			"close managed VM cgroup control after write",
			deps.close(context.WithoutCancel(ctx), fd),
		)
	}()

	for {
		if err := ctx.Err(); err != nil {
			return instanceAtomicError("write "+spec.name+" control", err)
		}
		count, err := deps.write(ctx, fd, value)
		if errors.Is(err, unix.EINTR) && count == 0 {
			continue
		}
		if err != nil {
			return instanceAtomicError("write "+spec.name+" control", err)
		}
		if count < 0 || count > len(value) {
			return instanceAtomicError(spec.name+" control write count is invalid", nil)
		}
		if count == 0 {
			return instanceAtomicError(spec.name+" control write made no progress", nil)
		}
		if count != len(value) {
			return instanceAtomicError(spec.name+" control write was short", nil)
		}
		return nil
	}
}

func openManagedVMCgroupControl(
	ctx context.Context,
	deps managedVMCgroupDeps,
	directoryFD int,
	directory managedVMCgroupIdentity,
	control managedVMCgroupControlFile,
	write bool,
) (_ int, _ managedVMCgroupControlFileSpec, returnErr error) {
	if ctx == nil {
		return -1, managedVMCgroupControlFileSpec{}, instanceAtomicError(
			"open managed VM cgroup control: context is required",
			nil,
		)
	}
	if err := ctx.Err(); err != nil {
		return -1, managedVMCgroupControlFileSpec{}, instanceAtomicError("open managed VM cgroup control", err)
	}
	spec, err := managedVMCgroupControlSpec(control)
	if err != nil {
		return -1, managedVMCgroupControlFileSpec{}, err
	}
	if write && !spec.writable {
		return -1, managedVMCgroupControlFileSpec{}, instanceAtomicError(
			"managed VM cgroup control is not writable",
			nil,
		)
	}
	if deps.openAt2 == nil || deps.statx == nil || deps.close == nil {
		return -1, managedVMCgroupControlFileSpec{}, instanceAtomicError(
			"managed VM cgroup control open dependencies are incomplete",
			nil,
		)
	}
	flags := unix.O_RDONLY | unix.O_NONBLOCK | unix.O_CLOEXEC | unix.O_NOFOLLOW
	if write {
		flags = unix.O_WRONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW
	}
	how := unix.OpenHow{Flags: uint64(flags), Resolve: uint64(managedVMCgroupResolveFlags)}
	fd, err := deps.openAt2(ctx, directoryFD, spec.name, &how)
	if err != nil {
		return -1, managedVMCgroupControlFileSpec{}, instanceAtomicError("open "+spec.name+" control", err)
	}
	defer func() {
		if returnErr == nil {
			return
		}
		returnErr = appendInstanceOperationError(
			returnErr,
			"close rejected managed VM cgroup control",
			deps.close(context.WithoutCancel(ctx), fd),
		)
	}()
	identity, err := inspectManagedVMCgroupRegularFile(ctx, deps, fd, spec.name+" control", spec.mode)
	if err != nil {
		return -1, managedVMCgroupControlFileSpec{}, err
	}
	if identity.mount != directory.mount || identity.deviceMajor != directory.deviceMajor ||
		identity.deviceMinor != directory.deviceMinor {
		return -1, managedVMCgroupControlFileSpec{}, instanceAtomicError(
			spec.name+" control is outside the pinned cgroup-v2 mount",
			nil,
		)
	}
	return fd, spec, nil
}

func readAllManagedVMCgroupControl(
	ctx context.Context,
	deps managedVMCgroupDeps,
	fd int,
	description string,
) ([]byte, error) {
	const chunkBytes = 256
	raw := make([]byte, 0, chunkBytes)
	var offset int64
	for {
		if err := ctx.Err(); err != nil {
			return nil, instanceAtomicError("read "+description+" control", err)
		}
		remaining := managedVMCgroupControlMaxBytes + 1 - len(raw)
		readSize := min(chunkBytes, remaining)
		chunk := make([]byte, readSize)
		count, err := deps.pread(ctx, fd, chunk, offset)
		if errors.Is(err, unix.EINTR) && count == 0 {
			continue
		}
		if err != nil {
			return nil, instanceAtomicError("read "+description+" control", err)
		}
		if count < 0 || count > len(chunk) {
			return nil, instanceAtomicError(description+" control read count is invalid", nil)
		}
		if count == 0 {
			return raw, nil
		}
		raw = append(raw, chunk[:count]...)
		if len(raw) > managedVMCgroupControlMaxBytes {
			return nil, instanceAtomicError(description+" control exceeds its bound", nil)
		}
		if offset > math.MaxInt64-int64(count) {
			return nil, instanceAtomicError(description+" control read offset overflowed", nil)
		}
		offset += int64(count)
	}
}

func managedVMCgroupControlSpec(control managedVMCgroupControlFile) (managedVMCgroupControlFileSpec, error) {
	switch control {
	case managedVMCgroupControllersControl:
		return managedVMCgroupControlFileSpec{name: "cgroup.controllers", mode: 0o444}, nil
	case managedVMCgroupSubtreeControl:
		return managedVMCgroupControlFileSpec{name: "cgroup.subtree_control", mode: 0o644, writable: true}, nil
	case managedVMCgroupCPUMaxControl:
		return managedVMCgroupControlFileSpec{name: "cpu.max", mode: 0o644, writable: true}, nil
	case managedVMCgroupCPUWeightControl:
		return managedVMCgroupControlFileSpec{name: "cpu.weight", mode: 0o644, writable: true}, nil
	case managedVMCgroupMemoryHighControl:
		return managedVMCgroupControlFileSpec{name: "memory.high", mode: 0o644, writable: true}, nil
	case managedVMCgroupMemoryMaxControl:
		return managedVMCgroupControlFileSpec{name: "memory.max", mode: 0o644, writable: true}, nil
	case managedVMCgroupMemorySwapMaxControl:
		return managedVMCgroupControlFileSpec{name: "memory.swap.max", mode: 0o644, writable: true}, nil
	case managedVMCgroupPIDsMaxControl:
		return managedVMCgroupControlFileSpec{name: "pids.max", mode: 0o644, writable: true}, nil
	default:
		return managedVMCgroupControlFileSpec{}, instanceAtomicError("managed VM cgroup control is invalid", nil)
	}
}

func parseManagedVMCgroupControllerSet(raw []byte) (managedVMCgroupControllerSet, error) {
	seen := make(map[string]struct{})
	var controllers managedVMCgroupControllerSet
	start := -1
	for index := 0; index <= len(raw); index++ {
		atEnd := index == len(raw)
		if !atEnd && !isManagedVMCgroupControllerWhitespace(raw[index]) {
			if start < 0 {
				start = index
			}
			continue
		}
		if start < 0 {
			continue
		}
		token := string(raw[start:index])
		start = -1
		if !isManagedVMCgroupControllerToken(token) {
			return 0, fmt.Errorf("cgroup controller token is invalid")
		}
		if _, exists := seen[token]; exists {
			return 0, fmt.Errorf("cgroup controller token is duplicated")
		}
		seen[token] = struct{}{}
		switch token {
		case "cpu":
			controllers |= managedVMCgroupControllerCPU
		case "memory":
			controllers |= managedVMCgroupControllerMemory
		case "pids":
			controllers |= managedVMCgroupControllerPIDs
		}
	}
	return controllers, nil
}

func requireManagedVMCgroupControllers(
	available managedVMCgroupControllerSet,
	required managedVMCgroupControllerSet,
) error {
	if required == 0 || available&^managedVMCgroupKnownControllers != 0 ||
		required&^managedVMCgroupKnownControllers != 0 {
		return fmt.Errorf("cgroup controller set is invalid")
	}
	if available&required != required {
		return fmt.Errorf("required cgroup controller is missing")
	}
	return nil
}

func formatManagedVMCgroupControllerEnablement(controllers managedVMCgroupControllerSet) ([]byte, error) {
	if controllers == 0 || controllers&^managedVMCgroupKnownControllers != 0 {
		return nil, fmt.Errorf("cgroup controller set is invalid")
	}
	values := make([]string, 0, 3)
	if controllers&managedVMCgroupControllerCPU != 0 {
		values = append(values, "+cpu")
	}
	if controllers&managedVMCgroupControllerMemory != 0 {
		values = append(values, "+memory")
	}
	if controllers&managedVMCgroupControllerPIDs != 0 {
		values = append(values, "+pids")
	}
	return []byte(strings.Join(values, " ") + "\n"), nil
}

func parseManagedVMCgroupFiniteUint(raw []byte) (uint64, error) {
	if len(raw) < 2 || raw[len(raw)-1] != '\n' {
		return 0, fmt.Errorf("finite cgroup control is not canonical")
	}
	return parseManagedVMCgroupCanonicalUint(raw[:len(raw)-1])
}

func formatManagedVMCgroupFiniteUint(value uint64) []byte {
	return append(strconv.AppendUint([]byte{}, value, 10), '\n')
}

func parseManagedVMCgroupCPUMax(raw []byte) (managedVMCgroupCPUMax, error) {
	if len(raw) < 4 || raw[len(raw)-1] != '\n' {
		return managedVMCgroupCPUMax{}, fmt.Errorf("cpu.max is not canonical")
	}
	fields := strings.Split(string(raw[:len(raw)-1]), " ")
	if len(fields) != 2 {
		return managedVMCgroupCPUMax{}, fmt.Errorf("cpu.max must contain two fields")
	}
	quota, err := parseManagedVMCgroupCanonicalUint([]byte(fields[0]))
	if err != nil {
		return managedVMCgroupCPUMax{}, fmt.Errorf("cpu.max quota is invalid: %w", err)
	}
	period, err := parseManagedVMCgroupCanonicalUint([]byte(fields[1]))
	if err != nil {
		return managedVMCgroupCPUMax{}, fmt.Errorf("cpu.max period is invalid: %w", err)
	}
	if quota == 0 || period == 0 {
		return managedVMCgroupCPUMax{}, fmt.Errorf("cpu.max values must be positive")
	}
	return managedVMCgroupCPUMax{quotaMicros: quota, periodMicros: period}, nil
}

func formatManagedVMCgroupCPUMax(value managedVMCgroupCPUMax) ([]byte, error) {
	if value.quotaMicros == 0 || value.periodMicros == 0 {
		return nil, fmt.Errorf("cpu.max values must be positive")
	}
	raw := strconv.AppendUint([]byte{}, value.quotaMicros, 10)
	raw = append(raw, ' ')
	raw = strconv.AppendUint(raw, value.periodMicros, 10)
	return append(raw, '\n'), nil
}

func parseManagedVMCgroupCanonicalUint(raw []byte) (uint64, error) {
	if len(raw) == 0 || (len(raw) > 1 && raw[0] == '0') {
		return 0, fmt.Errorf("unsigned decimal is not canonical")
	}
	for _, character := range raw {
		if character < '0' || character > '9' {
			return 0, fmt.Errorf("unsigned decimal contains a non-digit")
		}
	}
	value, err := strconv.ParseUint(string(raw), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("unsigned decimal is out of range: %w", err)
	}
	return value, nil
}

func isManagedVMCgroupControllerWhitespace(character byte) bool {
	return character == ' ' || character == '\t' || character == '\n' || character == '\r'
}

func isManagedVMCgroupControllerToken(token string) bool {
	if token == "" || token[0] < 'a' || token[0] > 'z' {
		return false
	}
	for index := 1; index < len(token); index++ {
		character := token[index]
		if (character < 'a' || character > 'z') && (character < '0' || character > '9') && character != '_' {
			return false
		}
	}
	return true
}
