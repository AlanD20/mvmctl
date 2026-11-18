package firewall

import (
	"net/netip"
	"slices"
	"time"

	"github.com/google/nftables"
)

// floatingIPIntent contains root-resolved ownership, never an element timeout or permission to renew one.
type floatingIPIntent struct {
	id                    string
	networkID             string
	ipv4                  netip.Addr
	generation            uint64
	ownerVMID             string
	ownerLaunchGeneration uint64
	lifetime              FloatingIPLifetime
	leaseDuration         time.Duration
}

type endpointAddressVariant struct {
	endpoint endpointIntent
	suffix   string
}

func (compiler *generationCompiler) endpointAddressVariants(endpoint endpointIntent) []endpointAddressVariant {
	return compiler.addresses[endpoint.id]
}

func addressPairRuleSuffix(source, destination endpointAddressVariant) string {
	if source.suffix == "" && destination.suffix == "" {
		return ""
	}
	return ".from" + source.suffix + ".to" + destination.suffix
}

func validateFloatingIPIntent(
	resources []floatingIPIntent,
	networks map[string]networkIntent,
	endpoints map[string]endpointIntent,
	primaryAddresses map[string]string,
) error {
	identities := make(map[string]struct{}, len(resources))
	addresses := make(map[netip.Addr]struct{}, len(resources))
	for _, resource := range resources {
		network, found := networks[resource.networkID]
		if !validResourceID(resource.id) || !found || resource.generation == 0 ||
			!usableIPv4Address(network.subnet, resource.ipv4) || resource.ipv4 == network.hostIPv4 {
			return invalidGeneration("floating-IP reservation has invalid logical authority")
		}
		if _, found := identities[resource.id]; found {
			return invalidGeneration("duplicate floating-IP resource identity")
		}
		if _, found := addresses[resource.ipv4]; found {
			return invalidGeneration("duplicate floating-IP address")
		}
		if _, found := primaryAddresses[resource.networkID+"/"+resource.ipv4.String()]; found {
			return invalidGeneration("floating IP conflicts with a primary address")
		}
		if resource.ownerVMID == "" {
			if resource.ownerLaunchGeneration != 0 || resource.lifetime != "" || resource.leaseDuration != 0 {
				return invalidGeneration("unassociated floating IP has launch authority")
			}
		} else {
			owner, found := endpoints[resource.ownerVMID]
			if !found || owner.networkID != resource.networkID || resource.ownerLaunchGeneration == 0 ||
				!validFloatingIPLifetime(resource.lifetime, resource.leaseDuration) {
				return invalidGeneration("floating-IP owner is not an active endpoint in its network")
			}
		}
		identities[resource.id] = struct{}{}
		addresses[resource.ipv4] = struct{}{}
	}
	return nil
}

func (compiler *generationCompiler) compileFloatingIPs(intent generationIntent) error {
	if len(intent.floatingIPs) == 0 {
		return nil
	}
	compiler.generation.bridge.secondaryOwners = make(map[string][secondaryLeaseKeyBytes]byte)
	postrouting := compiler.generation.bridge.chains[2]
	for _, resource := range sortedCopy(intent.floatingIPs, func(resource floatingIPIntent) string { return resource.id }) {
		if resource.ownerVMID != "" {
			key, err := secondaryLeaseKey(compiler.endpoints[resource.ownerVMID].hostVeth, resource.ipv4)
			if err != nil {
				return err
			}
			compiler.generation.bridge.secondaryOwners[resource.id] = key
		}
		compiler.addRule(&compiler.generation.bridge, postrouting, "bridge.floating.destination."+resource.id,
			rejectSecondaryDestination(compiler.networks[resource.networkID], resource.ipv4))
	}
	return nil
}

func validateSecondaryLeaseSet(set *nftables.Set, table *nftables.Table) error {
	if set == nil {
		return invalidGeneration("secondary lease set is missing")
	}
	expected := newSecondaryLeaseSet(table)
	if set.Table != table || set.ID != expected.ID || set.Name != expected.Name ||
		set.Anonymous || set.Constant || set.Interval || set.AutoMerge || set.IsMap || set.Counter || set.Dynamic ||
		!set.HasTimeout || !set.Concatenation || set.Timeout != 0 || set.KeyType != expected.KeyType ||
		set.DataType != (nftables.SetDatatype{}) || set.KeyByteOrder != expected.KeyByteOrder ||
		set.Comment != "" || set.Size != 0 {
		return invalidGeneration("secondary lease set differs from the closed timed-set definition")
	}
	return nil
}

func encodeCanonicalFloatingIPs(encoder *generationDigestEncoder, resources []floatingIPIntent) {
	if len(resources) == 0 {
		return
	}
	resources = sortedCopy(resources, func(resource floatingIPIntent) string { return resource.id })
	encoder.count("intent.floating-ips", len(resources))
	for _, resource := range resources {
		encoder.beginRecord("intent.floating-ip")
		encoder.text("id", resource.id)
		encoder.text("network-id", resource.networkID)
		address := resource.ipv4.As16()
		encoder.bytes("ipv4", address[:])
		encoder.uint64("generation", resource.generation)
		encoder.text("owner-vm-id", resource.ownerVMID)
		encoder.uint64("owner-launch-generation", resource.ownerLaunchGeneration)
		encoder.text("lifetime", string(resource.lifetime))
		encoder.uint64("lease-duration-ns", uint64(resource.leaseDuration))
		encoder.endRecord("intent.floating-ip")
	}
}

func encodeCanonicalSecondarySet(encoder *generationDigestEncoder, table compiledTable) error {
	if table.secondarySet == nil {
		if len(table.secondaryOwners) != 0 {
			return invalidGeneration("secondary ownership has no timed set")
		}
		return nil
	}
	if table.table == nil || table.table.Family != nftables.TableFamilyBridge ||
		table.table.Name != managedBridgeTableName {
		return invalidGeneration("secondary lease set belongs to an invalid table")
	}
	if err := validateSecondaryLeaseSet(table.secondarySet, table.table); err != nil {
		return err
	}
	// Validation admits exactly this definition. ID1 is batch-local; element contents and decreasing expiry are
	// observed separately and must not cause ordinary rule updates to restart a timeout.
	encoder.beginRecord("output.secondary-set")
	encoder.text("name", table.secondarySet.Name)
	encoder.uint32("key-type", table.secondarySet.KeyType.GetNFTMagic())
	encoder.uint32("key-bytes", table.secondarySet.KeyType.Bytes)
	encoder.boolean("timeout", true)
	encoder.boolean("concatenation", true)
	encoder.text("key-byte-order", "big-endian")
	resources := make([]string, 0, len(table.secondaryOwners))
	for id := range table.secondaryOwners {
		resources = append(resources, id)
	}
	slices.Sort(resources)
	encoder.count("owners", len(resources))
	for _, id := range resources {
		key := table.secondaryOwners[id]
		encoder.text("resource-id", id)
		encoder.bytes("key", key[:])
	}
	encoder.endRecord("output.secondary-set")
	return nil
}
