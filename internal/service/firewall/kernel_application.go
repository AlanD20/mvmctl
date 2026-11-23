package firewall

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/google/nftables"
)

type kernelApplicationConnection interface {
	deleteTable(*nftables.Table)
	addTable(*nftables.Table)
	addChain(*nftables.Chain)
	deleteChain(*nftables.Chain)
	flushChain(*nftables.Chain)
	addLabelAddressSet(*nftables.Set, []nftables.SetElement) error
	deleteLabelAddressSet(*nftables.Set)
	addSecondarySet(*nftables.Set) error
	deleteSecondarySet(*nftables.Set)
	deleteSecondaryElements(*nftables.Set, []nftables.SetElement) error
	addSecondaryElements(*nftables.Set, []nftables.SetElement) error
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
	if lease == nil || lease.state == nil {
		return lease.applyKernelGenerationDuringApplication(
			ctx,
			connection,
			observedBefore,
			expectedBefore,
			target,
		)
	}
	state := lease.state
	state.applicationMu.Lock()
	defer state.applicationMu.Unlock()
	return lease.applyKernelGenerationDuringApplication(ctx, connection, observedBefore, expectedBefore, target)
}

// CALLER MUST HOLD: state.applicationMu.
func (lease *persistentGenerationLease) applyKernelGenerationDuringApplication(
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
	record, err := lease.state.kernelApplicationRecord()
	if err != nil {
		return kernelApplicationResult{}, errors.Join(err, connection.close())
	}
	if err := validateKernelApplicationReplacement(record, observedBefore, expectedBefore, target); err != nil {
		return kernelApplicationResult{}, errors.Join(err, connection.close())
	}

	retainTables := !expectedBefore.empty && !target.empty
	if !retainTables {
		for _, observed := range observedBefore.tables {
			connection.deleteTable(&nftables.Table{Family: observed.family, Name: observed.name})
		}
	}
	bridgeBefore, inetBefore := compiledTable{}, compiledTable{}
	if retainTables {
		bridgeBefore, inetBefore = expectedBefore.bridge, expectedBefore.inet
	}
	revoked := secondaryLeaseRevocations(expectedBefore, target, observedBefore)
	if err := queueKernelApplicationTable(connection, target.bridge, bridgeBefore, revoked); err != nil {
		return kernelApplicationResult{}, errors.Join(err, connection.close())
	}
	if err := queueKernelApplicationTable(connection, target.inet, inetBefore, nil); err != nil {
		return kernelApplicationResult{}, errors.Join(err, connection.close())
	}
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
	record, err := state.retainedGenerationRecord()
	if err != nil {
		return generationRecord{}, err
	}
	if record.lifecycle != generationRecordApplying {
		return generationRecord{}, generationAuthorityError(
			"persistent firewall generation lease has no admitted applying transition",
			nil,
			nil,
		)
	}
	return record, nil
}

func queueKernelApplicationTable(
	connection kernelApplicationConnection,
	table compiledTable,
	before compiledTable,
	revoked []nftables.SetElement,
) error {
	if table.table == nil {
		return nil
	}
	if before.table != nil {
		// Preserve kernel-owned lifetimes when changing rules. Recreating a timed set from its observed
		// remaining duration would restart that duration after an unbounded delay before the batch commits.
		for _, chain := range before.chains {
			connection.flushChain(chain)
			if !kernelApplicationHasChain(table, chain.Name) {
				connection.deleteChain(chain)
			}
		}
		for _, set := range before.labelAddressSets {
			if !hasLabelAddressSet(table.labelAddressSets, set.name) {
				connection.deleteLabelAddressSet(set.kernelSet(before.table))
			}
		}
		if before.secondarySet != nil && table.secondarySet == nil {
			connection.deleteSecondarySet(before.secondarySet)
		}
	} else {
		connection.addTable(table.table)
	}
	for _, chain := range table.chains {
		if !kernelApplicationHasChain(before, chain.Name) {
			connection.addChain(chain)
		}
	}
	if table.secondarySet != nil {
		if before.secondarySet == nil {
			if err := connection.addSecondarySet(table.secondarySet); err != nil {
				return generationAuthorityError("queue secondary lease set creation", err, nil)
			}
		} else if len(revoked) != 0 {
			if err := connection.deleteSecondaryElements(before.secondarySet, revoked); err != nil {
				return generationAuthorityError("queue exact secondary lease revocation", err, nil)
			}
		}
	}
	for _, set := range table.labelAddressSets {
		if hasLabelAddressSet(before.labelAddressSets, set.name) {
			continue
		}
		if err := connection.addLabelAddressSet(set.kernelSet(table.table), set.elements()); err != nil {
			return generationAuthorityError("queue label address set creation", err, nil)
		}
	}
	for _, rule := range table.rules {
		connection.addRule(rule)
	}
	return nil
}

func kernelApplicationHasChain(table compiledTable, name string) bool {
	for _, chain := range table.chains {
		if chain.Name == name {
			return true
		}
	}
	return false
}

func hasLabelAddressSet(sets []labelAddressSet, name string) bool {
	_, found := slices.BinarySearchFunc(sets, name, func(set labelAddressSet, name string) int {
		return strings.Compare(set.name, name)
	})
	return found
}
