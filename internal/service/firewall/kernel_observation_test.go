package firewall

import (
	"slices"
	"strings"
	"testing"

	"github.com/google/nftables"
	"github.com/google/nftables/expr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCompareKernelObservationAcceptsExactCompilerOutput(t *testing.T) {
	compiled, err := compileGeneration(compatibilityIntent())
	require.NoError(t, err)

	observed := kernelObservationFromCompiled(compiled)

	assert.NoError(t, compareKernelObservation(observed, compiled))
}

func TestCompareKernelObservationAcceptsExactEmptyState(t *testing.T) {
	compiled, err := compileGeneration(generationIntent{})
	require.NoError(t, err)

	assert.NoError(t, compareKernelObservation(kernelObservation{empty: true}, compiled))
}

func TestCompareKernelObservationRejectsNoncanonicalState(t *testing.T) {
	tests := map[string]func(*kernelObservation){
		"empty marker with managed state": func(observed *kernelObservation) {
			observed.empty = true
		},
		"generation identity": func(observed *kernelObservation) {
			observed.generationID = strings.Repeat("0", 64)
		},
		"extra table": func(observed *kernelObservation) {
			observed.tables = append(observed.tables, observed.tables[0])
		},
		"table order": func(observed *kernelObservation) {
			observed.tables[0], observed.tables[1] = observed.tables[1], observed.tables[0]
		},
		"table flags": func(observed *kernelObservation) {
			observed.tables[0].flags = 1
		},
		"chain order": func(observed *kernelObservation) {
			observed.tables[0].chains[0], observed.tables[0].chains[1] =
				observed.tables[0].chains[1], observed.tables[0].chains[0]
		},
		"chain hook": func(observed *kernelObservation) {
			observed.tables[0].chains[0].hook = *nftables.ChainHookOutput
		},
		"chain priority": func(observed *kernelObservation) {
			observed.tables[0].chains[0].priority = *nftables.ChainPriorityFilter
		},
		"chain type": func(observed *kernelObservation) {
			observed.tables[0].chains[0].chainType = nftables.ChainTypeNAT
		},
		"chain policy": func(observed *kernelObservation) {
			observed.tables[0].chains[0].policy = nftables.ChainPolicyDrop
		},
		"chain device": func(observed *kernelObservation) {
			observed.tables[0].chains[0].device = "eth0"
		},
		"extra rule": func(observed *kernelObservation) {
			observed.tables[0].rules = append(observed.tables[0].rules, observed.tables[0].rules[0])
		},
		"rule order": func(observed *kernelObservation) {
			observed.tables[0].rules[0], observed.tables[0].rules[1] =
				observed.tables[0].rules[1], observed.tables[0].rules[0]
		},
		"rule chain": func(observed *kernelObservation) {
			observed.tables[0].rules[0].chain = managedBridgeForwardChainName
		},
		"rule position": func(observed *kernelObservation) {
			observed.tables[0].rules[0].position = 1
		},
		"rule flags": func(observed *kernelObservation) {
			observed.tables[0].rules[0].flags = 1
		},
		"rule userdata": func(observed *kernelObservation) {
			observed.tables[0].rules[0].userdata = []byte("foreign")
		},
		"expression order": func(observed *kernelObservation) {
			observed.tables[0].rules[0].expressions[0], observed.tables[0].rules[0].expressions[1] =
				observed.tables[0].rules[0].expressions[1], observed.tables[0].rules[0].expressions[0]
		},
		"expression field": func(observed *kernelObservation) {
			meta := observed.tables[0].rules[0].expressions[0].(*expr.Meta)
			meta.Register++
		},
	}

	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			compiled, err := compileGeneration(compatibilityIntent())
			require.NoError(t, err)
			observed := kernelObservationFromCompiled(compiled)
			mutate(&observed)

			err = compareKernelObservation(observed, compiled)
			assert.Error(t, err)
			assert.ErrorContains(t, err, "kernel observation")
		})
	}
}

func TestCompareKernelObservationRejectsStateForEmptyCompilerOutput(t *testing.T) {
	compiled, err := compileGeneration(generationIntent{})
	require.NoError(t, err)

	nonempty, err := compileGeneration(compatibilityIntent())
	require.NoError(t, err)

	assert.Error(t, compareKernelObservation(kernelObservationFromCompiled(nonempty), compiled))
}

func kernelObservationFromCompiled(compiled compiledGeneration) kernelObservation {
	if compiled.empty {
		return kernelObservation{empty: true}
	}

	result := kernelObservation{generationID: compiled.id}
	for _, table := range []compiledTable{compiled.bridge, compiled.inet} {
		observedTable := kernelObservedTable{
			family: table.table.Family,
			name:   table.table.Name,
			flags:  table.table.Flags,
		}
		for _, chain := range table.chains {
			observedChain := kernelObservedChain{
				name:      chain.Name,
				chainType: chain.Type,
				device:    chain.Device,
			}
			if chain.Hooknum != nil {
				observedChain.hook = *chain.Hooknum
				observedChain.hookPresent = true
			}
			if chain.Priority != nil {
				observedChain.priority = *chain.Priority
				observedChain.priorityPresent = true
			}
			if chain.Policy != nil {
				observedChain.policy = *chain.Policy
			} else {
				// The kernel reports the effective accept policy for base chains even when creation omitted it.
				observedChain.policy = nftables.ChainPolicyAccept
			}
			observedChain.policyPresent = true
			observedTable.chains = append(observedTable.chains, observedChain)
		}
		for _, compiledRule := range table.rules {
			observedTable.rules = append(observedTable.rules, kernelObservedRule{
				table:       tableIdentity{family: compiledRule.rule.Table.Family, name: compiledRule.rule.Table.Name},
				chain:       compiledRule.rule.Chain.Name,
				position:    compiledRule.rule.Position,
				flags:       compiledRule.rule.Flags,
				userdata:    slices.Clone(compiledRule.rule.UserData),
				expressions: cloneObservedExpressions(compiledRule.rule.Exprs),
			})
		}
		result.tables = append(result.tables, observedTable)
	}
	return result
}

func cloneObservedExpressions(values []expr.Any) []expr.Any {
	result := make([]expr.Any, 0, len(values))
	for _, value := range values {
		switch typed := value.(type) {
		case *expr.Meta:
			copy := *typed
			result = append(result, &copy)
		case *expr.Masq:
			copy := *typed
			result = append(result, &copy)
		case *expr.Bitwise:
			copy := *typed
			copy.Mask = slices.Clone(typed.Mask)
			copy.Xor = slices.Clone(typed.Xor)
			result = append(result, &copy)
		case *expr.Cmp:
			copy := *typed
			copy.Data = slices.Clone(typed.Data)
			result = append(result, &copy)
		case *expr.Ct:
			copy := *typed
			result = append(result, &copy)
		case *expr.Payload:
			copy := *typed
			result = append(result, &copy)
		case *expr.Range:
			copy := *typed
			copy.FromData = slices.Clone(typed.FromData)
			copy.ToData = slices.Clone(typed.ToData)
			result = append(result, &copy)
		case *expr.Verdict:
			copy := *typed
			result = append(result, &copy)
		}
	}
	return result
}
