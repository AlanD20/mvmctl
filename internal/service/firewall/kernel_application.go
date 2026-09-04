package firewall

import (
	"context"
	"errors"

	"github.com/google/nftables"
)

type kernelApplicationConnection interface {
	deleteTable(*nftables.Table)
	addTable(*nftables.Table)
	addChain(*nftables.Chain)
	addRule(compiledRule)
	flush() error
	interrupt() error
	close() error
}

type kernelApplicationResult struct {
	commitAttempted bool
}

func (lease *persistentGenerationLease) applyKernelGeneration(
	ctx context.Context,
	connection kernelApplicationConnection,
	observedBefore kernelObservation,
	expectedBefore compiledGeneration,
	target compiledGeneration,
) (kernelApplicationResult, error) {
	if connection == nil {
		return kernelApplicationResult{}, generationAuthorityError(
			"kernel application connection is required",
			nil,
			nil,
		)
	}
	if ctx == nil {
		return kernelApplicationResult{}, errors.Join(
			generationAuthorityError("kernel application context is required", nil, nil),
			connection.close(),
		)
	}
	if err := ctx.Err(); err != nil {
		return kernelApplicationResult{}, generationAuthorityError(
			"apply kernel firewall generation",
			errors.Join(err, connection.close()),
			nil,
		)
	}
	if lease == nil || lease.state == nil {
		return kernelApplicationResult{}, errors.Join(
			generationAuthorityError("persistent firewall generation lease is unavailable", nil, nil),
			connection.close(),
		)
	}
	state := lease.state
	state.applicationMu.Lock()
	defer state.applicationMu.Unlock()
	record, err := state.kernelApplicationRecord()
	if err != nil {
		return kernelApplicationResult{}, errors.Join(err, connection.close())
	}
	if err := validateKernelApplicationReplacement(record, observedBefore, expectedBefore, target); err != nil {
		return kernelApplicationResult{}, errors.Join(err, connection.close())
	}

	for _, observed := range observedBefore.tables {
		connection.deleteTable(&nftables.Table{Family: observed.family, Name: observed.name})
	}
	queueKernelApplicationTable(connection, target.bridge)
	queueKernelApplicationTable(connection, target.inet)
	if err := ctx.Err(); err != nil {
		closeErr := connection.close()
		return kernelApplicationResult{}, generationAuthorityError(
			"apply kernel firewall generation",
			errors.Join(err, closeErr),
			nil,
		)
	}

	result := kernelApplicationResult{commitAttempted: true}
	interruptResult := make(chan error, 1)
	stopInterrupt := context.AfterFunc(ctx, func() {
		interruptResult <- connection.interrupt()
	})
	flushErr := connection.flush()
	var interruptErr error
	if !stopInterrupt() {
		interruptErr = <-interruptResult
	}
	closeErr := connection.close()
	if err := errors.Join(ctx.Err(), interruptErr, flushErr, closeErr); err != nil {
		return result, generationAuthorityError("apply atomic kernel firewall generation", err, nil)
	}
	return result, nil
}

func (state *persistentGenerationLeaseState) kernelApplicationRecord() (generationRecord, error) {
	state.mu.Lock()
	defer state.mu.Unlock()
	if !state.active || state.released {
		return generationRecord{}, generationAuthorityError(
			"persistent firewall generation lease is released",
			nil,
			nil,
		)
	}
	if state.mutationAmbiguous {
		return generationRecord{}, generationAuthorityError(
			"persistent firewall generation authority outcome is ambiguous",
			nil,
			nil,
		)
	}
	if state.global == nil || state.lock == nil || state.store == nil ||
		state.evidence == (generationRecordFileEvidence{}) {
		return generationRecord{}, generationAuthorityError(
			"persistent firewall generation lease does not retain complete mutation authority",
			nil,
			nil,
		)
	}
	if err := validateGenerationRecord(state.record); err != nil {
		return generationRecord{}, err
	}
	if state.record.lifecycle != generationRecordApplying {
		return generationRecord{}, generationAuthorityError(
			"persistent firewall generation lease has no admitted applying transition",
			nil,
			nil,
		)
	}
	return cloneGenerationRecord(state.record), nil
}

func queueKernelApplicationTable(connection kernelApplicationConnection, table compiledTable) {
	if table.table == nil {
		return
	}
	connection.addTable(table.table)
	for _, chain := range table.chains {
		connection.addChain(chain)
	}
	for _, rule := range table.rules {
		connection.addRule(rule)
	}
}
