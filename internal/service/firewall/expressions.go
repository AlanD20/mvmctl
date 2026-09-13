package firewall

import (
	"encoding/binary"
	"net/netip"

	"github.com/google/nftables/binaryutil"
	"github.com/google/nftables/expr"
)

const (
	etherTypeIPv4 uint16 = 0x0800
	etherTypeARP  uint16 = 0x0806
	nfprotoIPv4   byte   = 2
)

func matchInputInterface(name string) []expr.Any {
	return matchInterface(expr.MetaKeyIIFNAME, name)
}

func matchOutputInterface(name string) []expr.Any {
	return matchInterface(expr.MetaKeyOIFNAME, name)
}

func matchInterface(key expr.MetaKey, name string) []expr.Any {
	data := make([]byte, 16)
	copy(data, name)
	return []expr.Any{
		&expr.Meta{Key: key, Register: 1},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: data},
	}
}

func matchEtherType(etherType uint16) []expr.Any {
	data := etherTypeData(etherType)
	return []expr.Any{
		&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseLLHeader, Offset: 12, Len: 2},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: data},
	}
}

func rejectUnsupportedEtherType() []expr.Any {
	return []expr.Any{
		&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseLLHeader, Offset: 12, Len: 2},
		&expr.Cmp{Op: expr.CmpOpNeq, Register: 1, Data: etherTypeData(etherTypeIPv4)},
		&expr.Cmp{Op: expr.CmpOpNeq, Register: 1, Data: etherTypeData(etherTypeARP)},
		&expr.Verdict{Kind: expr.VerdictDrop},
	}
}

func etherTypeData(etherType uint16) []byte {
	data := make([]byte, 2)
	binary.BigEndian.PutUint16(data, etherType)
	return data
}

func matchIPv4Family() []expr.Any {
	return []expr.Any{
		&expr.Meta{Key: expr.MetaKeyNFPROTO, Register: 1},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{nfprotoIPv4}},
	}
}

func matchIPv4Address(offset uint32, address netip.Addr) []expr.Any {
	data := address.As4()
	return []expr.Any{
		&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseNetworkHeader, Offset: offset, Len: 4},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: data[:]},
	}
}

func rejectIPv4AddressMismatch(offset uint32, address netip.Addr) []expr.Any {
	data := address.As4()
	return []expr.Any{
		&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseNetworkHeader, Offset: offset, Len: 4},
		&expr.Cmp{Op: expr.CmpOpNeq, Register: 1, Data: data[:]},
	}
}

func matchARPAddress(offset uint32, address netip.Addr, operation expr.CmpOp) []expr.Any {
	data := address.As4()
	return []expr.Any{
		&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseNetworkHeader, Offset: offset, Len: 4},
		&expr.Cmp{Op: operation, Register: 1, Data: data[:]},
	}
}

func matchARPHeader(operation expr.CmpOp) []expr.Any {
	return []expr.Any{
		&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseNetworkHeader, Offset: 0, Len: 6},
		&expr.Cmp{Op: operation, Register: 1, Data: []byte{0, 1, 8, 0, 6, 4}},
	}
}

func matchARPOperation(operation expr.CmpOp) []expr.Any {
	return []expr.Any{
		&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseNetworkHeader, Offset: 6, Len: 2},
		&expr.Range{Op: operation, Register: 1, FromData: []byte{0, 1}, ToData: []byte{0, 2}},
	}
}

func matchSupportedARP() []expr.Any {
	// Linux struct arphdr places hardware/protocol types, their address lengths, then the opcode in eight bytes.
	// Ethernet/IPv4 request/reply is the only supported grammar. Target addresses remain unconstrained, but loading
	// the final target IPv4 proves the complete 28-byte payload exists: a truncated payload breaks the allow rule.
	// See https://www.rfc-editor.org/rfc/rfc826.html.
	return joinExpressions(
		matchARPHeader(expr.CmpOpEq),
		matchARPOperation(expr.CmpOpEq),
		[]expr.Any{&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseNetworkHeader, Offset: 24, Len: 4}},
	)
}

func matchIPv4Prefix(offset uint32, prefix netip.Prefix) []expr.Any {
	prefix = prefix.Masked()
	address := prefix.Addr().As4()
	if prefix.Bits() == 32 {
		return matchIPv4Address(offset, prefix.Addr())
	}

	mask := ipv4Mask(prefix.Bits())
	return []expr.Any{
		&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseNetworkHeader, Offset: offset, Len: 4},
		&expr.Bitwise{SourceRegister: 1, DestRegister: 1, Len: 4, Mask: mask, Xor: make([]byte, 4)},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: address[:]},
	}
}

func ipv4Mask(bits int) []byte {
	mask := make([]byte, 4)
	for index := range mask {
		remaining := bits - index*8
		switch {
		case remaining >= 8:
			mask[index] = 0xff
		case remaining > 0:
			mask[index] = byte(0xff << (8 - remaining))
		}
	}
	return mask
}

func matchMACSource(address macAddress, operation expr.CmpOp) []expr.Any {
	return []expr.Any{
		&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseLLHeader, Offset: 6, Len: 6},
		&expr.Cmp{Op: operation, Register: 1, Data: address[:]},
	}
}

func matchARPSourceMAC(address macAddress, operation expr.CmpOp) []expr.Any {
	return []expr.Any{
		&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseNetworkHeader, Offset: 8, Len: 6},
		&expr.Cmp{Op: operation, Register: 1, Data: address[:]},
	}
}

func matchProtocol(value protocol) []expr.Any {
	return []expr.Any{
		&expr.Meta{Key: expr.MetaKeyL4PROTO, Register: 1},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{byte(value)}},
	}
}

func matchDestinationPorts(ports portInterval) []expr.Any {
	first := make([]byte, 2)
	binary.BigEndian.PutUint16(first, ports.first)
	payload := &expr.Payload{DestRegister: 1, Base: expr.PayloadBaseTransportHeader, Offset: 2, Len: 2}
	if ports.first == ports.last {
		return []expr.Any{payload, &expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: first}}
	}

	last := make([]byte, 2)
	binary.BigEndian.PutUint16(last, ports.last)
	return []expr.Any{
		payload,
		&expr.Range{Op: expr.CmpOpEq, Register: 1, FromData: first, ToData: last},
	}
}

func acceptEstablishedRelated() []expr.Any {
	mask := binaryutil.NativeEndian.PutUint32(expr.CtStateBitESTABLISHED | expr.CtStateBitRELATED)
	return []expr.Any{
		&expr.Ct{Register: 1, Key: expr.CtKeySTATE},
		&expr.Bitwise{SourceRegister: 1, DestRegister: 1, Len: 4, Mask: mask, Xor: make([]byte, 4)},
		&expr.Cmp{Op: expr.CmpOpNeq, Register: 1, Data: make([]byte, 4)},
		&expr.Verdict{Kind: expr.VerdictAccept},
	}
}

func acceptVerdict() []expr.Any {
	return []expr.Any{&expr.Verdict{Kind: expr.VerdictAccept}}
}

func dropVerdict() []expr.Any {
	return []expr.Any{&expr.Verdict{Kind: expr.VerdictDrop}}
}

func joinExpressions(groups ...[]expr.Any) []expr.Any {
	count := 0
	for _, group := range groups {
		count += len(group)
	}

	result := make([]expr.Any, 0, count)
	for _, group := range groups {
		result = append(result, group...)
	}
	return result
}
