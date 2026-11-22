package firewall

import (
	"testing"

	"github.com/mdlayher/netlink"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func TestDecodeManagedSetsReadsStaticMembershipInBothFamilies(t *testing.T) {
	keys := [][4]byte{{10, 0, 0, 2}, {10, 0, 0, 3}}
	name := labelAddressSetName(keys)
	raw := kernelObservationMessages{labelElementReplies: make(map[managedSetRef][]netlink.Message)}
	for _, table := range managedTableIdentities {
		ref := managedSetRef{table: table, name: name}
		raw.setReplies = append(raw.setReplies, staticLabelSetReply(t, ref))
		raw.labelElementReplies[ref] = []netlink.Message{staticLabelElementReply(t, ref, keys[1], keys[0])}
	}
	observed := kernelObservation{tables: make([]kernelObservedTable, len(managedTableIdentities))}
	require.NoError(t, decodeManagedSets(raw, &observed, &kernelBaseSequence{}))
	for _, table := range observed.tables {
		assert.Equal(t, []labelAddressSet{{name: name, keys: keys}}, table.labelAddressSets)
		assert.Nil(t, table.secondarySet)
	}
}

func TestDecodeManagedSetsRejectsForeignMissingOrChangedMembership(t *testing.T) {
	keys := [][4]byte{{10, 0, 0, 2}, {10, 0, 0, 3}}
	ref := managedSetRef{table: managedTableIdentities[0], name: labelAddressSetName(keys)}
	for name, mutation := range map[string]func(*kernelObservationMessages){
		"missing dump": func(raw *kernelObservationMessages) { delete(raw.labelElementReplies, ref) },
		"wrong table": func(raw *kernelObservationMessages) {
			foreign := managedSetRef{table: managedTableIdentities[1], name: ref.name}
			raw.labelElementReplies[ref] = []netlink.Message{staticLabelElementReply(t, foreign, keys...)}
		},
		"extra member": func(raw *kernelObservationMessages) {
			raw.labelElementReplies[ref] = []netlink.Message{
				staticLabelElementReply(t, ref, keys[0], keys[1], [4]byte{10, 0, 0, 4}),
			}
		},
		"duplicate member": func(raw *kernelObservationMessages) {
			raw.labelElementReplies[ref] = []netlink.Message{staticLabelElementReply(t, ref, keys[0], keys[0])}
		},
		"unexpected timeout": func(raw *kernelObservationMessages) {
			mutateMessageAttributes(t, &raw.setReplies[0], func(attrs []netlink.Attribute) []netlink.Attribute {
				return append(attrs, netlink.Attribute{Type: unix.NFTA_SET_TIMEOUT, Data: testBigEndianUint64(100)})
			})
		},
	} {
		t.Run(name, func(t *testing.T) {
			raw := kernelObservationMessages{
				setReplies: []netlink.Message{staticLabelSetReply(t, ref)},
				labelElementReplies: map[managedSetRef][]netlink.Message{
					ref: {staticLabelElementReply(t, ref, keys...)},
				},
			}
			mutation(&raw)
			observed := kernelObservation{tables: make([]kernelObservedTable, len(managedTableIdentities))}
			require.Error(t, decodeManagedSets(raw, &observed, &kernelBaseSequence{}))
		})
	}
}

func staticLabelSetReply(t *testing.T, ref managedSetRef) netlink.Message {
	t.Helper()
	return testKernelMessage(t, unix.NFT_MSG_NEWSET, ref.table.family, netlink.Multi, []netlink.Attribute{
		{Type: unix.NFTA_SET_TABLE, Data: []byte(ref.table.name + "\x00")},
		{Type: unix.NFTA_SET_NAME, Data: []byte(ref.name + "\x00")},
		{Type: unix.NFTA_SET_KEY_TYPE, Data: testBigEndianUint32(7)},
		{Type: unix.NFTA_SET_KEY_LEN, Data: testBigEndianUint32(4)},
		{Type: unix.NFTA_SET_USERDATA, Data: []byte{0, 4, 2, 0, 0, 0}},
	})
}

func staticLabelElementReply(t *testing.T, ref managedSetRef, keys ...[4]byte) netlink.Message {
	t.Helper()
	elements := make([]netlink.Attribute, 0, len(keys))
	for _, key := range keys {
		value := mustMarshalKernelAttributes(t, []netlink.Attribute{{Type: unix.NFTA_DATA_VALUE, Data: key[:]}})
		entry := mustMarshalKernelAttributes(t, []netlink.Attribute{{Type: unix.NFTA_SET_ELEM_KEY, Data: value}})
		elements = append(elements, netlink.Attribute{Type: unix.NFTA_LIST_ELEM, Data: entry})
	}
	return testKernelMessage(t, unix.NFT_MSG_NEWSETELEM, ref.table.family, netlink.Multi, []netlink.Attribute{
		{Type: unix.NFTA_SET_ELEM_LIST_TABLE, Data: []byte(ref.table.name + "\x00")},
		{Type: unix.NFTA_SET_ELEM_LIST_SET, Data: []byte(ref.name + "\x00")},
		{Type: unix.NFTA_SET_ELEM_LIST_ELEMENTS, Data: mustMarshalKernelAttributes(t, elements)},
	})
}
