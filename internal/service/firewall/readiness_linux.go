package firewall

import (
	"context"
	"errors"
	"net/netip"
	"os"
	"runtime"
	"time"

	"golang.org/x/sys/unix"
)

// ProbeReadiness exercises the fixed production expression set in a fresh disposable network namespace.
// CRITICAL: Root administrator paths call this directly while holding exclusive host admission. It
// neither enters the dispatcher recursively nor opens or mutates a host firewall table or record.
func ProbeReadiness(ctx context.Context) (ReadinessResult, error) {
	if ctx == nil {
		return ReadinessResult{}, generationAuthorityError("firewall readiness context is required", nil, nil)
	}
	if os.Geteuid() != 0 {
		return ReadinessResult{}, generationAuthorityError("firewall readiness requires root authority", nil, nil)
	}
	probeCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	result := make(chan error, 1)
	// CRITICAL: Namespace changes run on a disposable locked thread. Failed restoration retires that
	// thread; it is never returned to the Go scheduler carrying the probe's network namespace.
	go func() { result <- probeReadinessOnThread(probeCtx) }()
	if err := <-result; err != nil {
		return ReadinessResult{}, preserveGenerationAuthorityError("probe firewall kernel capabilities", err)
	}
	if err := probeCtx.Err(); err != nil {
		return ReadinessResult{}, generationAuthorityError("firewall readiness context ended", err, nil)
	}
	return ReadinessResult{Ready: true}, nil
}

func probeReadinessOnThread(ctx context.Context) (returnErr error) {
	runtime.LockOSThread()
	observation := realKernelObservationNamespaceDeps()
	application := realKernelApplicationNamespaceDeps(observation)
	hostFD := -1
	probeFD := -1
	var hostIdentity generationNamespaceIdentity
	var kernel *kernelObservationNamespaceLease
	changed := false
	defer func() {
		cleanupCtx := context.WithoutCancel(ctx)
		if kernel != nil {
			returnErr = errors.Join(returnErr, kernel.Release(cleanupCtx))
		}
		if probeFD >= 0 {
			returnErr = errors.Join(returnErr, closeKernelApplicationNamespace(cleanupCtx, application, &probeFD))
		}
		restored := !changed
		if changed {
			var err error
			restored, err = restoreKernelApplicationNamespace(cleanupCtx, application, hostFD, hostIdentity)
			returnErr = errors.Join(returnErr, err)
		}
		returnErr = errors.Join(returnErr, closeKernelApplicationNamespace(cleanupCtx, application, &hostFD))
		if restored {
			runtime.UnlockOSThread()
		}
	}()
	if err := ctx.Err(); err != nil {
		return err
	}
	var err error
	hostFD, err = application.openCurrentNamespace(ctx)
	if err != nil {
		return err
	}
	hostIdentity, err = application.inspectNamespace(ctx, hostFD)
	if err != nil {
		return err
	}
	if err := unix.Unshare(unix.CLONE_NEWNET); err != nil {
		return err
	}
	changed = true
	probeFD, err = application.openCurrentNamespace(ctx)
	if err != nil {
		return err
	}
	probeIdentity, err := application.inspectNamespace(ctx, probeFD)
	if err != nil {
		return err
	}
	if probeIdentity == hostIdentity {
		return generationAuthorityError("firewall readiness namespace is not isolated", nil, nil)
	}
	kernel = &kernelObservationNamespaceLease{state: &kernelObservationNamespaceLeaseState{
		deps: observation, applicationDeps: application, fd: probeFD, identity: probeIdentity, active: true,
	}}
	probeFD = -1
	empty, err := compileGeneration(generationIntent{})
	if err != nil {
		return err
	}
	if err := verifyExactCommittedGeneration(ctx, kernel, empty); err != nil {
		return err
	}
	target, err := compileReadinessGeneration()
	if err != nil {
		return err
	}
	connection, err := newGoogleNFTablesApplicationConnection(ctx)
	if err != nil {
		return err
	}
	queueKernelApplicationTable(connection, target.bridge)
	queueKernelApplicationTable(connection, target.inet)
	if err := flushReadinessConnection(ctx, connection); err != nil {
		return err
	}
	if err := verifyExactCommittedGeneration(ctx, kernel, target); err != nil {
		return err
	}
	connection, err = newGoogleNFTablesApplicationConnection(ctx)
	if err != nil {
		return err
	}
	connection.deleteTable(target.bridge.table)
	connection.deleteTable(target.inet.table)
	if err := flushReadinessConnection(ctx, connection); err != nil {
		return err
	}
	return verifyExactCommittedGeneration(ctx, kernel, empty)
}

func flushReadinessConnection(ctx context.Context, connection kernelApplicationConnection) error {
	if err := ctx.Err(); err != nil {
		return errors.Join(err, connection.close())
	}
	interrupted := make(chan error, 1)
	stop := context.AfterFunc(ctx, func() { interrupted <- connection.interrupt() })
	flushErr := connection.flush()
	var interruptErr error
	if !stop() {
		interruptErr = <-interrupted
	}
	return errors.Join(ctx.Err(), flushErr, interruptErr, connection.close())
}

func compileReadinessGeneration() (compiledGeneration, error) {
	return compileGeneration(generationIntent{
		networks: []networkIntent{
			{
				id:              "probe-a",
				hostBridge:      "mvpbra",
				subnet:          netip.MustParsePrefix("10.253.0.0/24"),
				hostIPv4:        netip.MustParseAddr("10.253.0.1"),
				uplinkInterface: "mvpuplink",
				masquerade:      true,
			},
			{
				id:         "probe-b",
				hostBridge: "mvpbrb",
				subnet:     netip.MustParsePrefix("10.253.1.0/24"),
				hostIPv4:   netip.MustParseAddr("10.253.1.1"),
			},
		},
		endpoints: []endpointIntent{
			{
				id:        "probe-vm-a",
				networkID: "probe-a",
				hostVeth:  "mvpvetha",
				ipv4:      netip.MustParseAddr("10.253.0.2"),
				mac:       macAddress{2, 252, 0, 0, 0, 1},
			},
			{
				id:        "probe-vm-b",
				networkID: "probe-b",
				hostVeth:  "mvpvethb",
				ipv4:      netip.MustParseAddr("10.253.1.2"),
				mac:       macAddress{2, 252, 0, 0, 0, 2},
			},
		},
		vmGrants: []vmTrafficGrant{
			{
				id:              "probe-tcp",
				sourceVMID:      "probe-vm-a",
				destinationVMID: "probe-vm-b",
				protocol:        protocolTCP,
				ports:           portInterval{first: 22, last: 23},
			},
		},
		networkGrants: []networkTrafficGrant{
			{
				id:              "probe-udp",
				sourceNetworkID: "probe-a",
				destinationVMID: "probe-vm-b",
				protocol:        protocolUDP,
				ports:           portInterval{first: 53, last: 53},
			},
		},
		hostServices: []hostServiceIntent{
			{
				id:              "probe-host",
				sourceNetworkID: "probe-a",
				protocol:        protocolTCP,
				ports:           portInterval{first: 80, last: 80},
			},
		},
	})
}
