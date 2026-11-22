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

func (compiler *generationCompiler) labelAddressSet(
	family nftables.TableFamily, members []EndpointBinding,
) (labelAddressSet, error) {
	keys := make([]labelAddressKey, 0, len(members))
	for _, member := range members {
		var interfaceName string
		switch family {
		case nftables.TableFamilyBridge:
			interfaceName = member.HostVeth.Name
		case nftables.TableFamilyINet:
			interfaceName = member.ManagedBridge.Name
		default:
			return labelAddressSet{}, invalidGeneration("label set requires a managed table family")
		}
		// Use the same owner-bound addresses as exact VM grants. Bridge ingress
		// and destination guards still require the live lease for each alias.
		for _, variant := range compiler.endpointAddressVariants(compiler.endpoints[member.VMID]) {
			key, err := secondaryLeaseKey(interfaceName, variant.endpoint.ipv4)
			if err != nil {
				return labelAddressSet{}, err
			}
			keys = append(keys, key)
		}
	}
	if len(keys) == 0 {
		return labelAddressSet{}, invalidGeneration("label address set membership is empty")
	}
	return newLabelAddressSet(keys), nil
}

func newLabelAddressSet(keys []labelAddressKey) labelAddressSet {
	slices.SortFunc(keys, compareLabelAddress)
	keys = slices.Compact(keys)
	return labelAddressSet{name: labelAddressSetName(keys), keys: keys}
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
