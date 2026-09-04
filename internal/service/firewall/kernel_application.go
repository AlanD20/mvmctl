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

func applyKernelGeneration(
	ctx context.Context,
	connection kernelApplicationConnection,
	record generationRecord,
	observedBefore kernelObservation,
	expectedBefore compiledGeneration,
	target compiledGeneration,
) (kernelApplicationResult, error) {
	if ctx == nil {
		return kernelApplicationResult{}, generationAuthorityError("kernel application context is required", nil, nil)
	}
	if connection == nil {
		return kernelApplicationResult{}, generationAuthorityError(
			"kernel application connection is required",
			nil,
			nil,
		)
	}
	if err := ctx.Err(); err != nil {
		return kernelApplicationResult{}, generationAuthorityError(
			"apply kernel firewall generation",
			errors.Join(err, connection.close()),
			nil,
		)
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
