package firewall

import (
	"encoding/binary"

	"github.com/google/nftables"
	"github.com/google/nftables/expr"
)

type trafficDirection byte

const (
	trafficOriginal trafficDirection = iota
	trafficReply
)

func (direction trafficDirection) endpoints() (uint32, uint32, expr.MetaKey, expr.MetaKey) {
	if direction == trafficReply {
		return 16, 12, expr.MetaKeyOIFNAME, expr.MetaKeyIIFNAME
	}
	return 12, 16, expr.MetaKeyIIFNAME, expr.MetaKeyOIFNAME
}

func matchOriginalTraffic(direction trafficDirection, transport protocol, ports portInterval) []expr.Any {
	first := binary.BigEndian.AppendUint16(nil, ports.first)
	return joinExpressions(
		[]expr.Any{
			&expr.Ct{Register: 1, Key: expr.CtKeyDIRECTION},
			&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{byte(direction)}},
			&expr.Ct{Register: 1, Key: expr.CtKeyPROTOCOL},
			&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{byte(transport)}},
			&expr.Ct{Register: 1, Key: expr.CtKeyPROTODST},
		},
		matchPortInterval(ports, first),
	)
}

func matchLabelMember(key expr.MetaKey, offset uint32, set string) []expr.Any {
	return []expr.Any{
		&expr.Meta{Key: key, Register: 1},
		&expr.Payload{DestRegister: 2, Base: expr.PayloadBaseNetworkHeader, Offset: offset, Len: 4},
		&expr.Lookup{SourceRegister: 1, SetName: set},
	}
}

func (compiler *generationCompiler) compileLabelTraffic(
	memberships []labelTrafficMembership,
	table *compiledTable,
	forward *nftables.Chain,
	prefix string,
	matchFamily []expr.Any,
) error {
	sets := make(map[string]labelAddressSet)
	for _, membership := range memberships {
		source, err := compiler.labelAddressSet(table.table.Family, membership.sources)
		if err != nil {
			return err
		}
		destination, err := compiler.labelAddressSet(table.table.Family, membership.destinations)
		if err != nil {
			return err
		}
		sets[source.name], sets[destination.name] = source, destination
		policy := membership.policy
		compiler.addDirectionalTrafficRules(
			table,
			forward,
			prefix+policy.ID,
			func(direction trafficDirection) []expr.Any {
				sourceOffset, destinationOffset, sourceKey, destinationKey := direction.endpoints()
				return joinExpressions(
					matchFamily,
					matchLabelMember(sourceKey, sourceOffset, source.name),
					matchLabelMember(destinationKey, destinationOffset, destination.name),
					matchOriginalTraffic(
						direction,
						protocol(policy.Protocol),
						portInterval{policy.Ports.First, policy.Ports.Last},
					),
					acceptVerdict(),
				)
			},
		)
	}
	for _, set := range sets {
		table.labelAddressSets = append(table.labelAddressSets, set)
	}
	table.labelAddressSets = sortedCopy(table.labelAddressSets, func(set labelAddressSet) string { return set.name })
	return nil
}

func (compiler *generationCompiler) addDirectionalTrafficRules(
	table *compiledTable,
	chain *nftables.Chain,
	id string,
	expressions func(trafficDirection) []expr.Any,
) {
	compiler.addRule(table, chain, id, expressions(trafficOriginal))
	compiler.addRule(table, chain, id+".reply", expressions(trafficReply))
}
