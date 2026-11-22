package firewall

import (
	"bytes"
	"slices"

	"github.com/google/nftables"
	"github.com/google/nftables/binaryutil"

	"mvmctl/internal/lib/crypto"
)

const labelAddressSetPrefix = "ls-"

type labelAddressSet struct {
	name string
	keys [][4]byte
}

func newLabelAddressSet(endpoints []EndpointBinding) (labelAddressSet, error) {
	keys := make([][4]byte, 0, len(endpoints))
	for _, endpoint := range endpoints {
		if !endpoint.IPv4.Is4() {
			return labelAddressSet{}, invalidGeneration("label set requires admitted IPv4 endpoints")
		}
		keys = append(keys, endpoint.IPv4.As4())
	}
	slices.SortFunc(keys, compareLabelAddress)
	keys = slices.Compact(keys)
	if len(keys) == 0 {
		return labelAddressSet{}, invalidGeneration("label address set membership is empty")
	}
	return labelAddressSet{name: labelAddressSetName(keys), keys: keys}, nil
}

func labelAddressSetName(keys [][4]byte) string {
	data := make([]byte, 0, len(keys)*4)
	for _, key := range keys {
		data = append(data, key[:]...)
	}
	return labelAddressSetPrefix + crypto.SHA256(data)
}

func compareLabelAddress(left, right [4]byte) int {
	return bytes.Compare(left[:], right[:])
}

func (set labelAddressSet) kernelSet(table *nftables.Table) *nftables.Set {
	return &nftables.Set{
		Table: table, Name: set.name, KeyType: nftables.TypeIPAddr, KeyByteOrder: binaryutil.BigEndian,
	}
}

func (set labelAddressSet) elements() []nftables.SetElement {
	elements := make([]nftables.SetElement, 0, len(set.keys))
	for _, key := range set.keys {
		elements = append(elements, nftables.SetElement{Key: slices.Clone(key[:])})
	}
	return elements
}

func validateLabelAddressSets(sets []labelAddressSet) error {
	for index, set := range sets {
		if len(set.keys) == 0 || set.name != labelAddressSetName(set.keys) ||
			(index > 0 && sets[index-1].name >= set.name) {
			return invalidGeneration("compiled label address set identity is invalid")
		}
		for index := 1; index < len(set.keys); index++ {
			if compareLabelAddress(set.keys[index-1], set.keys[index]) >= 0 {
				return invalidGeneration("compiled label address set keys must be sorted and unique")
			}
		}
	}
	return nil
}

func encodeCanonicalLabelAddressSets(encoder *generationDigestEncoder, sets []labelAddressSet) error {
	if err := validateLabelAddressSets(sets); err != nil {
		return err
	}
	if len(sets) == 0 {
		return nil
	}
	encoder.count("label-address-sets", len(sets))
	for _, set := range sets {
		encoder.beginRecord("label-address-set")
		encoder.text("name", set.name)
		encoder.count("keys", len(set.keys))
		for _, key := range set.keys {
			encoder.bytes("ipv4", key[:])
		}
		encoder.endRecord("label-address-set")
	}
	return nil
}
