package firewall

import (
	"bytes"

	"github.com/google/nftables"
	"github.com/google/nftables/binaryutil"
	"github.com/mdlayher/netlink"
	"golang.org/x/sys/unix"
)

// Linux UAPI set attributes absent from some versions of x/sys.
const (
	secondarySetHandle      = uint16(16)
	secondarySetPad         = uint16(14)
	secondarySetDescConcat  = uint16(2)
	secondarySetFieldLen    = uint16(1)
	secondarySetBackendType = uint16(19)
	secondarySetCount       = uint16(20)
)

// decodeSecondaryLeaseSet accepts only the closed bridge lease-set metadata grammar.
// Runtime transaction IDs and handles are checked but deliberately not returned as authority.
func decodeSecondaryLeaseSet(message netlink.Message, sequence *kernelBaseSequence) (*nftables.Set, error) {
	family, baseSequence, attrs, err := decodeKernelMessage(
		message,
		unix.NFT_MSG_NEWSET,
		netlink.Multi,
		"secondary lease set",
		map[uint16]kernelAttributeRule{
			unix.NFTA_SET_TABLE:       {required: true},
			unix.NFTA_SET_NAME:        {required: true},
			unix.NFTA_SET_FLAGS:       {required: true},
			unix.NFTA_SET_KEY_TYPE:    {required: true},
			unix.NFTA_SET_KEY_LEN:     {required: true},
			unix.NFTA_SET_DESC:        {required: true},
			unix.NFTA_SET_USERDATA:    {required: true},
			unix.NFTA_SET_POLICY:      {},
			unix.NFTA_SET_ID:          {},
			unix.NFTA_SET_TIMEOUT:     {},
			unix.NFTA_SET_GC_INTERVAL: {},
			secondarySetHandle:        {},
			secondarySetPad:           {zeroLength: true},
			secondarySetBackendType:   {},
			secondarySetCount:         {},
		},
	)
	if err != nil {
		return nil, err
	}
	table, err := kernelAttributeString(attrs[unix.NFTA_SET_TABLE], "secondary lease set table")
	if err != nil {
		return nil, err
	}
	name, err := kernelAttributeString(attrs[unix.NFTA_SET_NAME], "secondary lease set name")
	if err != nil {
		return nil, err
	}
	if family != nftables.TableFamilyBridge || table != "mvmctl_bridge" || name != "secondary_ipv4" {
		return nil, invalidKernelObservation("secondary lease set identity is invalid")
	}
	keyType := nftables.MustConcatSetType(nftables.TypeIFName, nftables.TypeIPAddr)
	for attrType, want := range map[uint16]uint32{
		unix.NFTA_SET_FLAGS:    unix.NFT_SET_TIMEOUT | nftables.NFT_SET_CONCAT,
		unix.NFTA_SET_KEY_TYPE: keyType.GetNFTMagic(),
		unix.NFTA_SET_KEY_LEN:  20,
	} {
		value, valueErr := kernelAttributeUint32(attrs[attrType], "secondary lease set metadata")
		if valueErr != nil {
			return nil, valueErr
		}
		if value != want {
			return nil, invalidKernelObservation("secondary lease set metadata differs from closed definition")
		}
	}
	for _, attrType := range []uint16{unix.NFTA_SET_POLICY, unix.NFTA_SET_GC_INTERVAL} {
		if attr, exists := attrs[attrType]; exists {
			value, valueErr := kernelAttributeUint32(attr, "secondary lease set default")
			if valueErr != nil {
				return nil, valueErr
			}
			if value != 0 {
				return nil, invalidKernelObservation("secondary lease set default is nonzero")
			}
		}
	}
	if attr, exists := attrs[unix.NFTA_SET_TIMEOUT]; exists {
		value, valueErr := kernelAttributeUint64(attr, "secondary lease set timeout")
		if valueErr != nil {
			return nil, valueErr
		}
		if value != 0 {
			return nil, invalidKernelObservation("secondary lease set has a default timeout")
		}
	}
	if attr, exists := attrs[unix.NFTA_SET_ID]; exists {
		value, valueErr := kernelAttributeUint32(attr, "secondary lease set transaction ID")
		if valueErr != nil {
			return nil, valueErr
		}
		if value == 0 {
			return nil, invalidKernelObservation("secondary lease set transaction ID is zero")
		}
	}
	if attr, exists := attrs[secondarySetHandle]; exists {
		value, valueErr := kernelAttributeUint64(attr, "secondary lease set handle")
		if valueErr != nil {
			return nil, valueErr
		}
		if value == 0 {
			return nil, invalidKernelObservation("secondary lease set handle is zero")
		}
	}
	// Linux reports its selected backend and current element count as runtime diagnostics.
	// Backend names may be rendered as pointers when symbol names are unavailable.
	if attr, exists := attrs[secondarySetBackendType]; exists {
		if _, err := kernelAttributeString(attr, "secondary lease set backend type"); err != nil {
			return nil, err
		}
	}
	if attr, exists := attrs[secondarySetCount]; exists {
		if _, err := kernelAttributeUint32(attr, "secondary lease set element count"); err != nil {
			return nil, err
		}
	}
	// v0.3.0 AddSet emits one libnftnl TLV: KEYBYTEORDER, length4, little-endian value2 (big endian).
	if !bytes.Equal(attrs[unix.NFTA_SET_USERDATA].Data, []byte{0, 4, 2, 0, 0, 0}) {
		return nil, invalidKernelObservation("secondary lease set userdata differs from closed definition")
	}
	if err := decodeSecondaryLeaseSetDescriptor(attrs[unix.NFTA_SET_DESC].Data); err != nil {
		return nil, err
	}
	if err := sequence.observe(baseSequence); err != nil {
		return nil, err
	}
	return &nftables.Set{
		Table: &nftables.Table{Family: nftables.TableFamilyBridge, Name: "mvmctl_bridge"},
		Name:  "secondary_ipv4", HasTimeout: true, Concatenation: true,
		KeyType: keyType, KeyByteOrder: binaryutil.BigEndian,
	}, nil
}

func decodeSecondaryLeaseSetDescriptor(raw []byte) error {
	attrs, err := decodeUniqueKernelAttributes(raw, "secondary lease set descriptor", map[uint16]kernelAttributeRule{
		unix.NFTA_SET_DESC_SIZE: {},
		secondarySetDescConcat:  {required: true},
	})
	if err != nil {
		return err
	}
	if attr, exists := attrs[unix.NFTA_SET_DESC_SIZE]; exists {
		size, sizeErr := kernelAttributeUint32(attr, "secondary lease set size")
		if sizeErr != nil {
			return sizeErr
		}
		if size != 0 {
			return invalidKernelObservation("secondary lease set has an explicit size")
		}
	}
	fields, err := decodeKernelAttributeList(attrs[secondarySetDescConcat].Data, "secondary lease set concatenation")
	if err != nil {
		return err
	}
	if len(fields) != 2 {
		return invalidKernelObservation("secondary lease set concatenation field count is invalid")
	}
	// v0.3.0 AddSet uses byte widths16 and4, with NFTA_LIST_ELEM wrappers.
	for index, want := range []uint32{16, 4} {
		field := fields[index]
		if kernelAttributeType(field) != unix.NFTA_LIST_ELEM || kernelAttributeFlags(field) != 0 {
			return invalidKernelObservation("secondary lease set concatenation field wrapper is invalid")
		}
		fieldAttrs, fieldErr := decodeUniqueKernelAttributes(field.Data, "secondary lease set field",
			map[uint16]kernelAttributeRule{secondarySetFieldLen: {required: true}})
		if fieldErr != nil {
			return fieldErr
		}
		width, widthErr := kernelAttributeUint32(fieldAttrs[secondarySetFieldLen], "secondary lease set field width")
		if widthErr != nil {
			return widthErr
		}
		if width != want {
			return invalidKernelObservation("secondary lease set concatenation field width is invalid")
		}
	}
	return nil
}
