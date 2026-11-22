package firewall

import (
	"bytes"
	"encoding/binary"
	"slices"
	"strings"

	"github.com/google/nftables"
	"github.com/mdlayher/netlink"
	"golang.org/x/sys/unix"
)

type managedSetRef struct {
	table tableIdentity
	name  string
}

var observedSetAttributes = map[uint16]kernelAttributeRule{
	unix.NFTA_SET_TABLE:       {required: true},
	unix.NFTA_SET_NAME:        {required: true},
	unix.NFTA_SET_FLAGS:       {},
	unix.NFTA_SET_KEY_TYPE:    {required: true},
	unix.NFTA_SET_KEY_LEN:     {required: true},
	unix.NFTA_SET_USERDATA:    {required: true},
	unix.NFTA_SET_DESC:        {},
	unix.NFTA_SET_POLICY:      {},
	unix.NFTA_SET_ID:          {},
	unix.NFTA_SET_TIMEOUT:     {},
	unix.NFTA_SET_GC_INTERVAL: {},
	secondarySetHandle:        {},
	secondarySetPad:           {zeroLength: true},
	secondarySetBackendType:   {},
	secondarySetCount:         {},
}

func decodeManagedSetHeader(
	message netlink.Message,
) (managedSetRef, uint16, map[uint16]netlink.Attribute, error) {
	family, seq, attrs, err := decodeKernelMessage(message, unix.NFT_MSG_NEWSET, netlink.Multi,
		"managed set", observedSetAttributes)
	if err != nil {
		return managedSetRef{}, 0, nil, err
	}
	table, err := kernelAttributeString(attrs[unix.NFTA_SET_TABLE], "managed set table")
	if err != nil {
		return managedSetRef{}, 0, nil, err
	}
	name, err := kernelAttributeString(attrs[unix.NFTA_SET_NAME], "managed set name")
	if err != nil {
		return managedSetRef{}, 0, nil, err
	}
	identity := tableIdentity{family: family, name: table}
	if !slices.Contains(managedTableIdentities[:], identity) {
		return managedSetRef{}, 0, nil, invalidKernelObservation("set has foreign table identity")
	}
	if identity == managedTableIdentities[0] && name == secondaryLeaseSetName {
		return managedSetRef{table: identity, name: name}, seq, attrs, nil
	}
	if !validLabelAddressSetName(name) {
		return managedSetRef{}, 0, nil, invalidKernelObservation("managed table contains an unknown set")
	}
	return managedSetRef{table: identity, name: name}, seq, attrs, nil
}

func decodeStaticLabelSetMetadata(attrs map[uint16]netlink.Attribute) error {
	for kind, want := range map[uint16]uint32{
		unix.NFTA_SET_KEY_TYPE: nftables.TypeIPAddr.GetNFTMagic(), unix.NFTA_SET_KEY_LEN: 4,
	} {
		got, err := kernelAttributeUint32(attrs[kind], "static label set metadata")
		if err != nil {
			return err
		}
		if got != want {
			return invalidKernelObservation("static label set metadata differs from closed definition")
		}
	}
	// The kernel omits the attribute for a static set with zero flags.
	if attr, exists := attrs[unix.NFTA_SET_FLAGS]; exists {
		flags, err := kernelAttributeUint32(attr, "static label set flags")
		if err != nil || flags != 0 {
			return invalidKernelObservation("static label set has unexpected flags")
		}
	}
	if !bytes.Equal(attrs[unix.NFTA_SET_USERDATA].Data, []byte{0, 4, 2, 0, 0, 0}) {
		return invalidKernelObservation("static label set byte order differs")
	}
	for _, kind := range []uint16{unix.NFTA_SET_TIMEOUT, unix.NFTA_SET_GC_INTERVAL} {
		if _, exists := attrs[kind]; exists {
			return invalidKernelObservation("static label set has timer metadata")
		}
	}
	if attr, exists := attrs[unix.NFTA_SET_DESC]; exists {
		desc, err := decodeUniqueKernelAttributes(attr.Data, "static label set descriptor",
			map[uint16]kernelAttributeRule{unix.NFTA_SET_DESC_SIZE: {}})
		if err != nil {
			return err
		}
		if size, exists := desc[unix.NFTA_SET_DESC_SIZE]; exists {
			value, err := kernelAttributeUint32(size, "static label set size")
			if err != nil || value != 0 {
				return invalidKernelObservation("static label set has unexpected capacity")
			}
		}
	}
	if attr, exists := attrs[unix.NFTA_SET_POLICY]; exists {
		policy, err := kernelAttributeUint32(attr, "static label set policy")
		if err != nil || policy != 0 {
			return invalidKernelObservation("static label set has unexpected policy")
		}
	}
	for _, kind := range []uint16{unix.NFTA_SET_ID, secondarySetCount} {
		if attr, exists := attrs[kind]; exists {
			if _, err := kernelAttributeUint32(attr, "static label set runtime attribute"); err != nil {
				return err
			}
		}
	}
	if attr, exists := attrs[secondarySetHandle]; exists {
		if _, err := kernelAttributeUint64(attr, "static label set handle"); err != nil {
			return err
		}
	}
	if attr, exists := attrs[secondarySetBackendType]; exists {
		if _, err := kernelAttributeString(attr, "static label set backend"); err != nil {
			return err
		}
	}
	return nil
}

func decodeStaticLabelSetElements(
	ref managedSetRef, messages []netlink.Message, sequence *kernelBaseSequence,
) (labelAddressSet, error) {
	set := labelAddressSet{name: ref.name}
	for _, message := range messages {
		family, seq, attrs, err := decodeKernelMessage(message, unix.NFT_MSG_NEWSETELEM, netlink.Multi,
			"static label set elements", map[uint16]kernelAttributeRule{
				unix.NFTA_SET_ELEM_LIST_TABLE:    {required: true},
				unix.NFTA_SET_ELEM_LIST_SET:      {required: true},
				unix.NFTA_SET_ELEM_LIST_ELEMENTS: {required: true},
			})
		if err != nil {
			return labelAddressSet{}, err
		}
		table, err := kernelAttributeString(attrs[unix.NFTA_SET_ELEM_LIST_TABLE], "static element table")
		if err != nil {
			return labelAddressSet{}, err
		}
		name, err := kernelAttributeString(attrs[unix.NFTA_SET_ELEM_LIST_SET], "static element set")
		if err != nil {
			return labelAddressSet{}, err
		}
		if family != ref.table.family || table != ref.table.name || name != ref.name {
			return labelAddressSet{}, invalidKernelObservation("static label set element binding differs")
		}
		if err := sequence.observe(seq); err != nil {
			return labelAddressSet{}, err
		}
		raw := attrs[unix.NFTA_SET_ELEM_LIST_ELEMENTS].Data
		for len(raw) != 0 {
			if len(raw) < 4 || len(set.keys) >= maxKernelObservationRules {
				return labelAddressSet{}, invalidKernelObservation("static label set elements exceed framing limits")
			}
			length := int(binary.NativeEndian.Uint16(raw[:2]))
			aligned := (length + 3) &^ 3
			if length < 4 || aligned > len(raw) {
				return labelAddressSet{}, invalidKernelObservation("static label set element length is invalid")
			}
			key, err := decodeStaticLabelElement(raw[:aligned])
			if err != nil {
				return labelAddressSet{}, err
			}
			set.keys = append(set.keys, key)
			raw = raw[aligned:]
		}
	}
	slices.SortFunc(set.keys, compareLabelAddress)
	if err := validateLabelAddressSets([]labelAddressSet{set}); err != nil {
		return labelAddressSet{}, invalidKernelObservation("static label set contains invalid keys or content identity")
	}
	return set, nil
}

func decodeStaticLabelElement(raw []byte) ([4]byte, error) {
	var result [4]byte
	wrappers, err := decodeKernelAttributeList(raw, "static label element wrapper")
	if err != nil {
		return result, err
	}
	if len(wrappers) != 1 || kernelAttributeType(wrappers[0]) != unix.NFTA_LIST_ELEM ||
		kernelAttributeFlags(wrappers[0]) != 0 {
		return result, invalidKernelObservation("static label element wrapper is invalid")
	}
	attrs, err := decodeUniqueKernelAttributes(wrappers[0].Data, "static label element",
		map[uint16]kernelAttributeRule{unix.NFTA_SET_ELEM_KEY: {required: true}})
	if err != nil {
		return result, err
	}
	key, err := decodeKernelDataValue(attrs[unix.NFTA_SET_ELEM_KEY].Data, "static label element key")
	if err != nil {
		return result, err
	}
	if len(key) != len(result) {
		return result, invalidKernelObservation("static label element key is not IPv4")
	}
	copy(result[:], key)
	return result, nil
}

func decodeManagedSets(
	raw kernelObservationMessages, observed *kernelObservation, sequence *kernelBaseSequence,
) error {
	if observed.empty && len(raw.setReplies) != 0 {
		return invalidKernelObservation("managed set exists without its table")
	}
	seenLabels := make(map[managedSetRef]struct{}, len(raw.labelElementReplies))
	var secondaryPresent bool
	for _, reply := range raw.setReplies {
		ref, seq, attrs, err := decodeManagedSetHeader(reply)
		if err != nil {
			return err
		}
		if ref.name == secondaryLeaseSetName {
			if secondaryPresent {
				return invalidKernelObservation("duplicate secondary lease set")
			}
			set, err := decodeSecondaryLeaseSet(reply, sequence)
			if err != nil {
				return err
			}
			elements, err := decodeSecondaryLeaseElements(raw.secondaryElementReplies, sequence)
			if err != nil {
				return err
			}
			observed.tables[0].secondarySet, observed.tables[0].secondaryElements = set, elements
			secondaryPresent = true
			continue
		}
		if err := sequence.observe(seq); err != nil {
			return err
		}
		if err := decodeStaticLabelSetMetadata(attrs); err != nil {
			return err
		}
		if _, duplicate := seenLabels[ref]; duplicate {
			return invalidKernelObservation("duplicate static label set")
		}
		messages, found := raw.labelElementReplies[ref]
		if !found {
			return invalidKernelObservation("static label set elements were not observed")
		}
		set, err := decodeStaticLabelSetElements(ref, messages, sequence)
		if err != nil {
			return err
		}
		seenLabels[ref] = struct{}{}
		tableIndex := slices.Index(managedTableIdentities[:], ref.table)
		observed.tables[tableIndex].labelAddressSets = append(observed.tables[tableIndex].labelAddressSets, set)
	}
	if len(seenLabels) != len(raw.labelElementReplies) || (!secondaryPresent && len(raw.secondaryElementReplies) != 0) {
		return invalidKernelObservation("set elements exist without their admitted set")
	}
	for index := range observed.tables {
		sets := observed.tables[index].labelAddressSets
		slices.SortFunc(sets, func(left, right labelAddressSet) int {
			return strings.Compare(left.name, right.name)
		})
	}
	return nil
}
