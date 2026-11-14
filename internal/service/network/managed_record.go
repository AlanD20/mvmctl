package network

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/netip"
	"strings"

	"golang.org/x/sys/unix"
)

const managedNetworkSchemaVersion = uint32(2)

type managedNetworkConfiguration struct {
	l3     managedNetworkL3
	nat    bool
	uplink *selectedUplink
}

type managedNetworkRecordWire struct {
	SchemaVersion uint32                   `json:"schema_version"`
	OwnerUID      uint32                   `json:"owner_uid"`
	NetworkID     string                   `json:"network_id"`
	BootID        string                   `json:"boot_id"`
	ManagedBridge networkAuthorityLinkWire `json:"managed_bridge"`
	Prefix        string                   `json:"prefix"`
	Gateway       string                   `json:"gateway"`
	NAT           bool                     `json:"nat"`
	Uplink        *managedUplinkWire       `json:"uplink"`
}

type managedUplinkWire struct {
	Link       networkAuthorityLinkWire `json:"link"`
	Kind       string                   `json:"kind"`
	DeviceType uint16                   `json:"device_type"`
	Alias      string                   `json:"alias"`
	Master     int                      `json:"master"`
	Gateway    string                   `json:"gateway"`
	Source     string                   `json:"source"`
	Protocol   uint8                    `json:"protocol"`
	Scope      uint8                    `json:"scope"`
	Metric     *uint32                  `json:"metric"`
}

var managedNetworkObjectFields = map[string]map[string]struct{}{
	"": networkAuthorityFields("schema_version", "owner_uid", "network_id", "boot_id", "managed_bridge",
		"prefix", "gateway", "nat", "uplink"),
	"managed_bridge": networkAuthorityFields("name", "ifindex", "namespace"),
	"link":           networkAuthorityFields("name", "ifindex", "namespace"),
	"namespace":      networkAuthorityFields("device", "inode"),
	"uplink": networkAuthorityFields("link", "kind", "device_type", "alias", "master", "gateway", "source",
		"protocol", "scope", "metric"),
}

func validateManagedNetworkConfiguration(record networkAuthorityRecord) error {
	configuration := record.configuration
	if configuration == nil {
		return networkAuthorityCorruptError("managed network canonical configuration is missing", nil)
	}
	gateway := configuration.l3.gateway.value
	canonical, err := admitManagedNetworkL3(managedNetworkL3Input{
		prefix: configuration.l3.prefix.value, gateway: &gateway,
	})
	if err != nil || canonical != configuration.l3 {
		return networkAuthorityCorruptError("managed network L3 configuration is not canonical", err)
	}
	if !configuration.nat {
		if configuration.uplink != nil {
			return networkAuthorityCorruptError("network without NAT contains uplink authority", nil)
		}
		return nil
	}
	uplink := configuration.uplink
	if uplink == nil || uplink.link.namespace != record.managedBridge.namespace || uplink.link.ifIndex <= 0 ||
		uplink.link.name == "" || len(uplink.link.name) > linuxInterfaceNameMax ||
		uplink.deviceType == unix.ARPHRD_LOOPBACK || uplink.route.ifIndex != uplink.link.ifIndex ||
		uplink.route.destination != netip.MustParsePrefix("0.0.0.0/0") || uplink.route.kind != unix.RTN_UNICAST ||
		uplink.route.table != unix.RT_TABLE_MAIN || isManagedUplinkLink(routeLinkObservation{
		name: uplink.link.name, alias: uplink.alias,
	}) {
		return networkAuthorityCorruptError("managed network selected uplink evidence is invalid", nil)
	}
	if (uplink.route.hasGateway && !uplink.route.gateway.Is4()) ||
		(uplink.route.hasSource && !uplink.route.source.Is4()) {
		return networkAuthorityCorruptError("managed network uplink IPv4 evidence is invalid", nil)
	}
	return nil
}

func networkLinkWire(link linkIdentity) networkAuthorityLinkWire {
	return networkAuthorityLinkWire{Name: link.name.String(), IfIndex: link.ifIndex,
		Namespace: networkAuthorityNamespaceWire{Device: link.namespace.device, Inode: link.namespace.inode}}
}

func networkLinkFromWire(wire networkAuthorityLinkWire) linkIdentity {
	return linkIdentity{name: linkName(wire.Name), ifIndex: wire.IfIndex,
		namespace: namespaceIdentity{device: wire.Namespace.Device, inode: wire.Namespace.Inode}}
}

func encodeManagedNetworkRecord(record networkAuthorityRecord) ([]byte, error) {
	configuration := record.configuration
	wire := managedNetworkRecordWire{
		SchemaVersion: record.schemaVersion, OwnerUID: record.ownerUID, NetworkID: string(record.networkID),
		BootID: record.bootID, ManagedBridge: networkLinkWire(record.managedBridge),
		Prefix: configuration.l3.prefix.value.String(), Gateway: configuration.l3.gateway.value.String(),
		NAT: configuration.nat,
	}
	if uplink := configuration.uplink; uplink != nil {
		wire.Uplink = &managedUplinkWire{Link: networkLinkWire(uplink.link), Kind: uplink.kind,
			DeviceType: uplink.deviceType, Alias: uplink.alias, Master: uplink.master,
			Protocol: uplink.route.protocol, Scope: uplink.route.scope}
		if uplink.route.hasGateway {
			wire.Uplink.Gateway = uplink.route.gateway.String()
		}
		if uplink.route.hasSource {
			wire.Uplink.Source = uplink.route.source.String()
		}
		if uplink.route.hasMetric {
			metric := uplink.route.metric
			wire.Uplink.Metric = &metric
		}
	}
	raw, err := json.Marshal(wire)
	if err != nil || len(raw) > maxNetworkAuthorityRecordBytes {
		return nil, networkAuthorityCorruptError("encode canonical managed network record", err)
	}
	return raw, nil
}

func decodeManagedNetworkRecord(raw []byte) (networkAuthorityRecord, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := scanManagedNetworkObject(decoder, "", 0); err != nil {
		return networkAuthorityRecord{}, networkAuthorityCorruptError("decode strict managed network record", err)
	}
	if err := requireNetworkAuthorityJSONEOF(decoder); err != nil {
		return networkAuthorityRecord{}, err
	}
	var wire managedNetworkRecordWire
	if err := json.Unmarshal(raw, &wire); err != nil {
		return networkAuthorityRecord{}, networkAuthorityCorruptError("decode managed network record", err)
	}
	prefix, prefixErr := netip.ParsePrefix(wire.Prefix)
	gateway, gatewayErr := netip.ParseAddr(wire.Gateway)
	if prefixErr != nil || gatewayErr != nil || prefix.String() != wire.Prefix || gateway.String() != wire.Gateway {
		return networkAuthorityRecord{}, networkAuthorityCorruptError("network L3 text is not canonical", nil)
	}
	l3, err := admitManagedNetworkL3(managedNetworkL3Input{prefix: prefix, gateway: &gateway})
	if err != nil {
		return networkAuthorityRecord{}, err
	}
	record := networkAuthorityRecord{schemaVersion: wire.SchemaVersion, ownerUID: wire.OwnerUID,
		networkID: NetworkID(
			wire.NetworkID,
		), bootID: wire.BootID, managedBridge: networkLinkFromWire(wire.ManagedBridge),
		configuration: &managedNetworkConfiguration{l3: l3, nat: wire.NAT}}
	if source := wire.Uplink; source != nil {
		uplink := &selectedUplink{link: networkLinkFromWire(source.Link), kind: source.Kind,
			deviceType: source.DeviceType, alias: source.Alias, master: source.Master,
			route: routeIPv4Observation{destination: netip.MustParsePrefix("0.0.0.0/0"),
				table: unix.RT_TABLE_MAIN, kind: unix.RTN_UNICAST, protocol: source.Protocol,
				scope: source.Scope, ifIndex: source.Link.IfIndex}}
		if source.Gateway != "" {
			uplink.route.gateway, err = netip.ParseAddr(source.Gateway)
			if err != nil || uplink.route.gateway.String() != source.Gateway {
				return networkAuthorityRecord{}, networkAuthorityCorruptError("uplink gateway is not canonical", err)
			}
			uplink.route.hasGateway = true
		}
		if source.Source != "" {
			uplink.route.source, err = netip.ParseAddr(source.Source)
			if err != nil || uplink.route.source.String() != source.Source {
				return networkAuthorityRecord{}, networkAuthorityCorruptError("uplink source is not canonical", err)
			}
			uplink.route.hasSource = true
		}
		if source.Metric != nil {
			uplink.route.metric, uplink.route.hasMetric = *source.Metric, true
		}
		record.configuration.uplink = uplink
	}
	return record, validateNetworkAuthorityRecord(record)
}

func scanManagedNetworkObject(decoder *json.Decoder, name string, depth int) error {
	if depth > 4 {
		return fmt.Errorf("managed network JSON nesting exceeds its schema")
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	if name == "uplink" && token == nil {
		return nil
	}
	if token != json.Delim('{') {
		return fmt.Errorf("managed network %q must be an object", name)
	}
	allowed := managedNetworkObjectFields[name]
	seen := make(map[string]bool, len(allowed))
	for decoder.More() {
		key, err := decoder.Token()
		if err != nil {
			return err
		}
		field, ok := key.(string)
		if !ok {
			return fmt.Errorf("managed network object key must be text")
		}
		if _, ok := allowed[field]; !ok || seen[strings.ToLower(field)] {
			return fmt.Errorf("unknown or duplicate managed network field %q", field)
		}
		seen[strings.ToLower(field)] = true
		switch field {
		case "managed_bridge", "link", "namespace", "uplink":
			if err := scanManagedNetworkObject(decoder, field, depth+1); err != nil {
				return err
			}
		default:
			value, err := decoder.Token()
			if err != nil {
				return err
			}
			if _, nested := value.(json.Delim); nested || (value == nil && field != "metric") {
				return fmt.Errorf("managed network field %q has invalid scalar value", field)
			}
		}
	}
	if _, err := decoder.Token(); err != nil {
		return err
	}
	if len(seen) != len(allowed) {
		return fmt.Errorf("managed network object %q is missing fields", name)
	}
	return nil
}
