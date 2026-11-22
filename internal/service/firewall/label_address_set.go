package firewall

import (
	"bytes"
	"slices"
	"strings"

	"github.com/google/nftables"
	"github.com/google/nftables/binaryutil"

	"mvmctl/internal/lib/crypto"
)

const labelAddressSetPrefix = "ls-"

type labelAddressSet struct {
	name string
	keys []labelAddressKey
}

type labelAddressKey = [secondaryLeaseKeyBytes]byte

func newLabelAddressSet(family nftables.TableFamily, endpoints []EndpointBinding) (labelAddressSet, error) {
	keys := make([]labelAddressKey, 0, len(endpoints))
	for _, endpoint := range endpoints {
		var interfaceName string
		switch family {
		case nftables.TableFamilyBridge:
			interfaceName = endpoint.HostVeth.Name
		case nftables.TableFamilyINet:
			interfaceName = endpoint.ManagedBridge.Name
		default:
			return labelAddressSet{}, invalidGeneration("label set requires a managed table family")
		}
		key, err := secondaryLeaseKey(interfaceName, endpoint.IPv4)
		if err != nil {
			return labelAddressSet{}, err
		}
		keys = append(keys, key)
	}
	slices.SortFunc(keys, compareLabelAddress)
	keys = slices.Compact(keys)
	if len(keys) == 0 {
		return labelAddressSet{}, invalidGeneration("label address set membership is empty")
	}
	return labelAddressSet{name: labelAddressSetName(keys), keys: keys}, nil
}

func labelAddressSetName(keys []labelAddressKey) string {
	data := make([]byte, 0, len(keys)*secondaryLeaseKeyBytes)
	for _, key := range keys {
		data = append(data, key[:]...)
	}
	return labelAddressSetPrefix + crypto.SHA256(data)
}

func validLabelAddressSetName(name string) bool {
	return strings.HasPrefix(name, labelAddressSetPrefix) &&
		validateGenerationID(name[len(labelAddressSetPrefix):]) == nil
}

func compareLabelAddress(left, right labelAddressKey) int {
	return bytes.Compare(left[:], right[:])
}

func (set labelAddressSet) kernelSet(table *nftables.Table) *nftables.Set {
	return &nftables.Set{
		Table: table, Name: set.name, Concatenation: true,
		KeyType:      labelAddressSetKeyType(),
		KeyByteOrder: binaryutil.BigEndian,
	}
}

func labelAddressSetKeyType() nftables.SetDatatype {
	return nftables.MustConcatSetType(nftables.TypeIFName, nftables.TypeIPAddr)
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
		for index, key := range set.keys {
			if _, _, err := decodeSecondaryLeaseKey(key[:]); err != nil {
				return invalidGeneration("compiled label address key is outside its managed tuple grammar")
			}
			if index > 0 && compareLabelAddress(set.keys[index-1], key) >= 0 {
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
			encoder.bytes("interface-ipv4", key[:])
		}
		encoder.endRecord("label-address-set")
	}
	return nil
}
