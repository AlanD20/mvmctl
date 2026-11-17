package firewall

import (
	"bytes"
	"net/netip"

	"github.com/google/nftables"
	"github.com/google/nftables/binaryutil"
	"github.com/google/nftables/expr"
	"golang.org/x/sys/unix"
)

const (
	secondaryLeaseSetName  = "secondary_ipv4"
	secondaryLeaseKeyBytes = 20
)

func newSecondaryLeaseSet(table *nftables.Table) *nftables.Set {
	return &nftables.Set{
		Table: table, ID: 1, Name: secondaryLeaseSetName,
		HasTimeout: true, Concatenation: true,
		KeyType:      nftables.MustConcatSetType(nftables.TypeIFName, nftables.TypeIPAddr),
		KeyByteOrder: binaryutil.BigEndian,
	}
}

// secondaryLeaseKey binds an address to its observed host veth. A live key alone does not prove the VM incarnation;
// the root association effect must establish that identity before installing or renewing this exact key.
func secondaryLeaseKey(hostVeth string, address netip.Addr) ([secondaryLeaseKeyBytes]byte, error) {
	var key [secondaryLeaseKeyBytes]byte
	if !validInterfaceName(hostVeth) || !address.Is4() || !address.IsPrivate() {
		return key, invalidGeneration("secondary lease requires a managed interface and private IPv4 address")
	}
	copy(key[:16], hostVeth)
	ipv4 := address.As4()
	copy(key[16:], ipv4[:])
	return key, nil
}

func decodeSecondaryLeaseKey(raw []byte) (string, netip.Addr, error) {
	if len(raw) != secondaryLeaseKeyBytes {
		return "", netip.Addr{}, invalidKernelObservation("secondary lease key length is invalid")
	}
	terminator := bytes.IndexByte(raw[:16], 0)
	if terminator <= 0 || !bytes.Equal(raw[terminator:16], make([]byte, 16-terminator)) {
		return "", netip.Addr{}, invalidKernelObservation("secondary lease interface is not canonically terminated")
	}
	name := string(raw[:terminator])
	address := netip.AddrFrom4([4]byte(raw[16:]))
	if !validInterfaceName(name) || !address.IsPrivate() {
		return "", netip.Addr{}, invalidKernelObservation("secondary lease key is outside managed address grammar")
	}
	return name, address, nil
}

func rejectSecondaryIPv4Source(primary netip.Addr) []expr.Any {
	return joinExpressions(rejectIPv4AddressMismatch(12, primary), secondaryLeaseMiss(expr.MetaKeyIIFNAME, 12))
}

func rejectSecondaryARPSource(primary netip.Addr) []expr.Any {
	return joinExpressions(matchARPAddress(14, primary, expr.CmpOpNeq), secondaryLeaseMiss(expr.MetaKeyIIFNAME, 14))
}

func rejectSecondaryDestination(network networkIntent, address netip.Addr) []expr.Any {
	return joinExpressions(
		matchInterface(expr.MetaKeyBRIOIFNAME, network.hostBridge),
		matchEtherType(etherTypeIPv4), matchIPv4Address(16, address),
		secondaryLeaseMiss(expr.MetaKeyOIFNAME, 16), dropVerdict(),
	)
}

func secondaryLeaseMiss(interfaceKey expr.MetaKey, addressOffset uint32) []expr.Any {
	// Legacy register1 contains16 bytes; register2 immediately follows it. The named set consumes the full20-byte key.
	return []expr.Any{
		&expr.Meta{Key: interfaceKey, Register: 1},
		&expr.Payload{DestRegister: 2, Base: expr.PayloadBaseNetworkHeader, Offset: addressOffset, Len: 4},
		&expr.Lookup{SourceRegister: 1, SetName: secondaryLeaseSetName, Invert: true},
	}
}

func decodeSecondaryLeaseLookup(raw []byte) (expr.Any, error) {
	attrs, err := decodeUniqueKernelAttributes(raw, "secondary lease lookup", map[uint16]kernelAttributeRule{
		unix.NFTA_LOOKUP_SET: {required: true}, unix.NFTA_LOOKUP_SREG: {required: true},
		unix.NFTA_LOOKUP_FLAGS: {required: true},
	})
	if err != nil {
		return nil, err
	}
	name, err := kernelAttributeString(attrs[unix.NFTA_LOOKUP_SET], "secondary lease lookup set")
	if err != nil {
		return nil, err
	}
	register, err := kernelAttributeUint32(attrs[unix.NFTA_LOOKUP_SREG], "secondary lease lookup register")
	if err != nil {
		return nil, err
	}
	flags, err := kernelAttributeUint32(attrs[unix.NFTA_LOOKUP_FLAGS], "secondary lease lookup flags")
	if err != nil {
		return nil, err
	}
	if name != secondaryLeaseSetName || register != 1 || flags != unix.NFT_LOOKUP_F_INV {
		return nil, invalidKernelObservation("secondary lease lookup differs from closed guard")
	}
	return &expr.Lookup{SourceRegister: register, SetName: name, Invert: true}, nil
}
