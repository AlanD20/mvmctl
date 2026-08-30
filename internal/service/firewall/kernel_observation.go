package firewall

import (
	"bytes"
	"fmt"

	"github.com/google/nftables"
	"github.com/google/nftables/expr"
)

type kernelObservation struct {
	empty        bool
	generationID string
	tables       []kernelObservedTable
}

type kernelObservedTable struct {
	family nftables.TableFamily
	name   string
	flags  uint32
	chains []kernelObservedChain
	rules  []kernelObservedRule
}

type kernelObservedChain struct {
	name            string
	hook            nftables.ChainHook
	hookPresent     bool
	priority        nftables.ChainPriority
	priorityPresent bool
	chainType       nftables.ChainType
	policy          nftables.ChainPolicy
	policyPresent   bool
	device          string
}

type kernelObservedRule struct {
	table       tableIdentity
	chain       string
	position    uint64
	flags       uint32
	userdata    []byte
	expressions []expr.Any
}

// compareKernelObservation treats every compiler-projected field as authority. Kernel-assigned handles and use
// counters are admitted by the raw decoder but deliberately excluded, matching the generation digest contract.
func compareKernelObservation(observed kernelObservation, expected compiledGeneration) error {
	if expected.empty {
		if !observed.empty || observed.generationID != "" || len(observed.tables) != 0 {
			return invalidKernelObservation("managed nftables state exists for an empty generation")
		}
		return nil
	}
	if observed.empty {
		return invalidKernelObservation("managed nftables state is absent for a nonempty generation")
	}
	if observed.generationID != expected.id {
		return invalidKernelObservation("generation identity does not match compiler output")
	}

	expectedTables := [...]compiledTable{expected.bridge, expected.inet}
	if len(observed.tables) != len(expectedTables) {
		return invalidKernelObservation("table count does not match compiler output")
	}
	for index := range expectedTables {
		if err := compareKernelObservedTable(observed.tables[index], expectedTables[index], expected.id); err != nil {
			return err
		}
	}
	return nil
}

func compareKernelObservedTable(observed kernelObservedTable, expected compiledTable, generationID string) error {
	if expected.table == nil || observed.family != expected.table.Family || observed.name != expected.table.Name ||
		observed.flags != expected.table.Flags {
		return invalidKernelObservation("table identity or flags do not match compiler output")
	}
	if len(observed.chains) != len(expected.chains) {
		return invalidKernelObservation(
			fmt.Sprintf("chain count for table %q does not match compiler output", observed.name),
		)
	}
	for index := range expected.chains {
		if !kernelObservedChainMatches(observed.chains[index], expected.chains[index]) {
			return invalidKernelObservation(fmt.Sprintf(
				"chain %d for table %q does not match compiler output",
				index,
				observed.name,
			))
		}
	}

	if len(observed.rules) != len(expected.rules) {
		return invalidKernelObservation(
			fmt.Sprintf("rule count for table %q does not match compiler output", observed.name),
		)
	}
	for index := range expected.rules {
		if err := compareKernelObservedRule(observed.rules[index], expected.rules[index], generationID); err != nil {
			return invalidKernelObservation(fmt.Sprintf(
				"rule %d for table %q does not match compiler output: %v",
				index,
				observed.name,
				err,
			))
		}
	}
	return nil
}

func kernelObservedChainMatches(observed kernelObservedChain, expected *nftables.Chain) bool {
	if expected == nil || observed.name != expected.Name || observed.chainType != expected.Type ||
		observed.device != expected.Device {
		return false
	}
	if observed.hookPresent != (expected.Hooknum != nil) ||
		(observed.hookPresent && observed.hook != *expected.Hooknum) {
		return false
	}
	if observed.priorityPresent != (expected.Priority != nil) ||
		(observed.priorityPresent && observed.priority != *expected.Priority) {
		return false
	}
	if !observed.policyPresent {
		return false
	}
	expectedPolicy := nftables.ChainPolicyAccept
	if expected.Policy != nil {
		expectedPolicy = *expected.Policy
	}
	return observed.policy == expectedPolicy
}

func compareKernelObservedRule(observed kernelObservedRule, expected compiledRule, generationID string) error {
	if expected.rule == nil || expected.rule.Table == nil || expected.rule.Chain == nil ||
		expected.rule.Chain.Table == nil {
		return invalidGeneration("compiled rule is absent from kernel comparison")
	}
	expectedTable := tableIdentity{family: expected.rule.Table.Family, name: expected.rule.Table.Name}
	if observed.table != expectedTable || observed.chain != expected.rule.Chain.Name ||
		observed.position != expected.rule.Position || observed.flags != expected.rule.Flags {
		return fmt.Errorf("identity, chain, position, or flags differ")
	}
	wantUserdata, err := encodeRuleIdentity(ruleIdentity{generation: generationID, rule: expected.identity})
	if err != nil {
		return err
	}
	if !bytes.Equal(observed.userdata, wantUserdata) || !bytes.Equal(expected.rule.UserData, wantUserdata) {
		return fmt.Errorf("userdata differs")
	}
	if !kernelExpressionsEqual(observed.expressions, expected.rule.Exprs) {
		return fmt.Errorf("expression sequence differs")
	}
	return nil
}

// kernelExpressionsEqual is a second closed field audit beside generation hashing. A new compiler-emitted expression
// cannot become observable until both the digest and raw inspection contracts account for all of its fields.
func kernelExpressionsEqual(observed []expr.Any, expected []expr.Any) bool {
	if len(observed) != len(expected) {
		return false
	}
	for index := range expected {
		if !kernelExpressionEqual(observed[index], expected[index]) {
			return false
		}
	}
	return true
}

func kernelExpressionEqual(observed expr.Any, expected expr.Any) bool {
	switch want := expected.(type) {
	case *expr.Meta:
		got, ok := observed.(*expr.Meta)
		return ok && got != nil && want != nil && *got == *want
	case *expr.Masq:
		got, ok := observed.(*expr.Masq)
		return ok && got != nil && want != nil && *got == *want
	case *expr.Bitwise:
		got, ok := observed.(*expr.Bitwise)
		return ok && got != nil && want != nil && got.SourceRegister == want.SourceRegister &&
			got.DestRegister == want.DestRegister && got.Len == want.Len && bytes.Equal(got.Mask, want.Mask) &&
			bytes.Equal(got.Xor, want.Xor)
	case *expr.Cmp:
		got, ok := observed.(*expr.Cmp)
		return ok && got != nil && want != nil && got.Op == want.Op && got.Register == want.Register &&
			bytes.Equal(got.Data, want.Data)
	case *expr.Ct:
		got, ok := observed.(*expr.Ct)
		return ok && got != nil && want != nil && *got == *want
	case *expr.Payload:
		got, ok := observed.(*expr.Payload)
		return ok && got != nil && want != nil && *got == *want
	case *expr.Range:
		got, ok := observed.(*expr.Range)
		return ok && got != nil && want != nil && got.Op == want.Op && got.Register == want.Register &&
			bytes.Equal(got.FromData, want.FromData) && bytes.Equal(got.ToData, want.ToData)
	case *expr.Verdict:
		got, ok := observed.(*expr.Verdict)
		return ok && got != nil && want != nil && *got == *want
	default:
		return false
	}
}

func invalidKernelObservation(reason string) error {
	return invalidGeneration("kernel observation is invalid: " + reason)
}
