package firewall

import "context"

func (lease *kernelObservationNamespaceLease) observeGeneration(ctx context.Context) (kernelObservation, error) {
	factory, err := lease.snapshotTransportFactory()
	if err != nil {
		return kernelObservation{}, err
	}
	return collectKernelObservationSnapshot(ctx, factory)
}

func (lease *kernelObservationNamespaceLease) applyGenerationDuringApplication(
	ctx context.Context,
	persistent *persistentGenerationLease,
	observedBefore kernelObservation,
	expectedBefore compiledGeneration,
	target compiledGeneration,
) (kernelApplicationResult, error) {
	connection, err := lease.newApplicationConnection(ctx)
	if err != nil {
		return kernelApplicationResult{}, err
	}
	return persistent.applyKernelGenerationDuringApplication(ctx, connection, observedBefore, expectedBefore, target)
}

var _ generationKernelApplication = (*kernelObservationNamespaceLease)(nil)
