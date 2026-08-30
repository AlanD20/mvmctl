package firewall

import (
	"slices"
	"strings"
	"testing"

	"github.com/google/nftables"
	"github.com/google/nftables/expr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/pkg/errs"
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

// Rationale: Canonical comparison is the final admission boundary after raw decoding, so every compiler-projected
// identity, presence marker, field, count, and sequence order must fail with the firewall internal error contract.
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
		"missing table": func(observed *kernelObservation) {
			observed.tables = observed.tables[:1]
		},
		"table order": func(observed *kernelObservation) {
			observed.tables[0], observed.tables[1] = observed.tables[1], observed.tables[0]
		},
		"table flags": func(observed *kernelObservation) {
			observed.tables[0].flags = 1
		},
		"table family": func(observed *kernelObservation) {
			observed.tables[0].family = nftables.TableFamilyIPv4
		},
		"table name": func(observed *kernelObservation) {
			observed.tables[0].name = "foreign"
		},
		"missing chain": func(observed *kernelObservation) {
			observed.tables[0].chains = observed.tables[0].chains[:1]
		},
		"chain order": func(observed *kernelObservation) {
			observed.tables[0].chains[0], observed.tables[0].chains[1] =
				observed.tables[0].chains[1], observed.tables[0].chains[0]
		},
		"chain hook": func(observed *kernelObservation) {
			observed.tables[0].chains[0].hook = *nftables.ChainHookOutput
		},
		"chain name": func(observed *kernelObservation) {
			observed.tables[0].chains[0].name = "foreign"
		},
		"chain hook presence": func(observed *kernelObservation) {
			observed.tables[0].chains[0].hookPresent = false
		},
		"chain priority": func(observed *kernelObservation) {
			observed.tables[0].chains[0].priority = *nftables.ChainPriorityFilter
		},
		"chain priority presence": func(observed *kernelObservation) {
			observed.tables[0].chains[0].priorityPresent = false
		},
		"chain type": func(observed *kernelObservation) {
			observed.tables[0].chains[0].chainType = nftables.ChainTypeNAT
		},
		"chain policy": func(observed *kernelObservation) {
			observed.tables[0].chains[0].policy = nftables.ChainPolicyDrop
		},
		"chain policy presence": func(observed *kernelObservation) {
			observed.tables[0].chains[0].policyPresent = false
		},
		"chain device": func(observed *kernelObservation) {
			observed.tables[0].chains[0].device = "eth0"
		},
		"extra rule": func(observed *kernelObservation) {
			observed.tables[0].rules = append(observed.tables[0].rules, observed.tables[0].rules[0])
		},
		"missing rule": func(observed *kernelObservation) {
			observed.tables[0].rules = observed.tables[0].rules[:len(observed.tables[0].rules)-1]
		},
		"rule order": func(observed *kernelObservation) {
			observed.tables[0].rules[0], observed.tables[0].rules[1] =
				observed.tables[0].rules[1], observed.tables[0].rules[0]
		},
		"rule chain": func(observed *kernelObservation) {
			observed.tables[0].rules[0].chain = managedBridgeForwardChainName
		},
		"rule table": func(observed *kernelObservation) {
			observed.tables[0].rules[0].table.name = "foreign"
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
			requireKernelObservationRejected(t, err)
		})
	}
}

// Rationale: Empty compiler authority must not admit any managed kernel state, even when that state is internally
// canonical for another generation.
func TestCompareKernelObservationRejectsStateForEmptyCompilerOutput(t *testing.T) {
	compiled, err := compileGeneration(generationIntent{})
	require.NoError(t, err)

	nonempty, err := compileGeneration(compatibilityIntent())
	require.NoError(t, err)

	requireKernelObservationRejected(t, compareKernelObservation(kernelObservationFromCompiled(nonempty), compiled))
}

// Rationale: The closed expression comparator is a second field audit beside digesting. Every field of every admitted
// type, plus typed nils and types outside the compiler set, must be unequal.
func TestKernelExpressionEqualRejectsEveryComparedFieldAndUnknownType(t *testing.T) {
	tests := map[string]struct {
		observed expr.Any
		expected expr.Any
	}{
		"meta key":                {&expr.Meta{Key: expr.MetaKeyOIFNAME}, &expr.Meta{}},
		"meta source register":    {&expr.Meta{SourceRegister: true}, &expr.Meta{}},
		"meta register":           {&expr.Meta{Register: 1}, &expr.Meta{}},
		"masq random":             {&expr.Masq{Random: true}, &expr.Masq{}},
		"masq fully random":       {&expr.Masq{FullyRandom: true}, &expr.Masq{}},
		"masq persistent":         {&expr.Masq{Persistent: true}, &expr.Masq{}},
		"masq to ports":           {&expr.Masq{ToPorts: true}, &expr.Masq{}},
		"masq protocol min":       {&expr.Masq{RegProtoMin: 1}, &expr.Masq{}},
		"masq protocol max":       {&expr.Masq{RegProtoMax: 1}, &expr.Masq{}},
		"bitwise source":          {&expr.Bitwise{SourceRegister: 2}, &expr.Bitwise{}},
		"bitwise destination":     {&expr.Bitwise{DestRegister: 2}, &expr.Bitwise{}},
		"bitwise length":          {&expr.Bitwise{Len: 1}, &expr.Bitwise{}},
		"bitwise mask":            {&expr.Bitwise{Mask: []byte{1}}, &expr.Bitwise{}},
		"bitwise xor":             {&expr.Bitwise{Xor: []byte{1}}, &expr.Bitwise{}},
		"comparison operation":    {&expr.Cmp{Op: expr.CmpOpNeq}, &expr.Cmp{}},
		"comparison register":     {&expr.Cmp{Register: 1}, &expr.Cmp{}},
		"comparison data":         {&expr.Cmp{Data: []byte{1}}, &expr.Cmp{}},
		"conntrack register":      {&expr.Ct{Register: 1}, &expr.Ct{}},
		"conntrack source":        {&expr.Ct{SourceRegister: true}, &expr.Ct{}},
		"conntrack key":           {&expr.Ct{Key: expr.CtKeyMARK}, &expr.Ct{}},
		"conntrack direction":     {&expr.Ct{Direction: 1}, &expr.Ct{}},
		"payload operation":       {&expr.Payload{OperationType: 1}, &expr.Payload{}},
		"payload destination":     {&expr.Payload{DestRegister: 1}, &expr.Payload{}},
		"payload source":          {&expr.Payload{SourceRegister: 1}, &expr.Payload{}},
		"payload base":            {&expr.Payload{Base: expr.PayloadBaseNetworkHeader}, &expr.Payload{}},
		"payload offset":          {&expr.Payload{Offset: 1}, &expr.Payload{}},
		"payload length":          {&expr.Payload{Len: 1}, &expr.Payload{}},
		"payload checksum type":   {&expr.Payload{CsumType: 1}, &expr.Payload{}},
		"payload checksum offset": {&expr.Payload{CsumOffset: 1}, &expr.Payload{}},
		"payload checksum flags":  {&expr.Payload{CsumFlags: 1}, &expr.Payload{}},
		"range operation":         {&expr.Range{Op: expr.CmpOpNeq}, &expr.Range{}},
		"range register":          {&expr.Range{Register: 1}, &expr.Range{}},
		"range lower":             {&expr.Range{FromData: []byte{1}}, &expr.Range{}},
		"range upper":             {&expr.Range{ToData: []byte{1}}, &expr.Range{}},
		"verdict kind":            {&expr.Verdict{Kind: expr.VerdictAccept}, &expr.Verdict{}},
		"verdict chain":           {&expr.Verdict{Chain: "foreign"}, &expr.Verdict{}},
		"typed nil observed":      {(*expr.Meta)(nil), &expr.Meta{}},
		"typed nil expected":      {&expr.Meta{}, (*expr.Meta)(nil)},
		"unknown expected type":   {&expr.Counter{}, &expr.Counter{}},
		"unknown observed type":   {&expr.Counter{}, &expr.Meta{}},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			assert.False(t, kernelExpressionEqual(tc.observed, tc.expected))
		})
	}
}

// kernelObservationFromCompiled creates an independently mutable canonical observation for comparison tests.
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
				flags:       compiledRule.rule.Flags,
				userdata:    slices.Clone(compiledRule.rule.UserData),
				expressions: cloneObservedExpressions(compiledRule.rule.Exprs),
			})
		}
		result.tables = append(result.tables, observedTable)
	}
	return result
}

// cloneObservedExpressions deep-copies every admitted expression so mutations cannot alter compiler output in place.
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

// requireKernelObservationRejected proves that every fail-closed path retains the firewall domain error contract.
func requireKernelObservationRejected(t *testing.T, err error) {
	t.Helper()
	require.Error(t, err)
	domainErr := errs.AsDomainError(err)
	require.NotNil(t, domainErr)
	assert.Equal(t, errs.CodeNetworkFirewallFailed, domainErr.Code)
	assert.Equal(t, errs.ClassInternal, domainErr.Class)
	assert.Empty(t, domainErr.Entity)
}
