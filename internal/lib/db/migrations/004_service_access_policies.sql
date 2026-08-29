-- Migration: 004_service_access_policies
-- Version: 4
-- Description: Routed service-access policy intent and derived firewall rule identity

PRAGMA foreign_keys = OFF;

CREATE TABLE service_access_policies (
    id TEXT PRIMARY KEY,
    source_network_id TEXT NOT NULL,
    destination_vm_id TEXT NOT NULL,
    protocol TEXT NOT NULL CHECK(protocol IN ('tcp', 'udp')),
    destination_port_start INTEGER NOT NULL CHECK(destination_port_start BETWEEN 1 AND 65535),
    destination_port_end INTEGER NOT NULL CHECK(destination_port_end BETWEEN destination_port_start AND 65535),
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP NOT NULL,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP NOT NULL,
    FOREIGN KEY (source_network_id) REFERENCES networks(id) ON DELETE CASCADE,
    FOREIGN KEY (destination_vm_id) REFERENCES vm_instances(id) ON DELETE CASCADE,
    UNIQUE(source_network_id, destination_vm_id, protocol, destination_port_start, destination_port_end)
);
CREATE INDEX idx_service_access_policies_source ON service_access_policies(source_network_id);
CREATE INDEX idx_service_access_policies_destination_vm ON service_access_policies(destination_vm_id);

ALTER TABLE iptables_rules RENAME TO iptables_rules_v3;
CREATE TABLE iptables_rules (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    table_name TEXT NOT NULL CHECK(table_name IN ('nat', 'filter')),
    chain_name TEXT NOT NULL CHECK(chain_name LIKE 'MVM-%'),
    rule_type TEXT NOT NULL CHECK(rule_type IN (
        'masquerade', 'forward_in', 'forward_out', 'nocloudnet_input',
        'service_access_allow', 'routed_managed_drop', 'host_managed_drop'
    )),
    protocol TEXT NOT NULL CHECK(protocol IN ('tcp', 'udp', 'icmp', 'all')),
    source TEXT NOT NULL,
    destination TEXT NOT NULL,
    in_interface TEXT NOT NULL,
    out_interface TEXT NOT NULL,
    target TEXT NOT NULL,
    sport INTEGER NOT NULL,
    dport INTEGER NOT NULL,
    dport_end INTEGER NOT NULL DEFAULT 0,
    network_id TEXT NOT NULL,
    service_access_policy_id TEXT NULL,
    comment_tag TEXT NULL,
    command_string TEXT NULL,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP NOT NULL,
    last_verified_at TIMESTAMP NULL,
    is_active INTEGER DEFAULT 1 NOT NULL CHECK(is_active IN (0, 1)),
    FOREIGN KEY (network_id) REFERENCES networks(id) ON DELETE CASCADE,
    FOREIGN KEY (service_access_policy_id) REFERENCES service_access_policies(id) ON DELETE CASCADE
);
INSERT INTO iptables_rules (
    id, table_name, chain_name, rule_type, protocol, source, destination, in_interface,
    out_interface, target, sport, dport, dport_end, network_id, comment_tag, command_string,
    created_at, last_verified_at, is_active
)
SELECT id, table_name, chain_name, rule_type, protocol, source, destination, in_interface,
       out_interface, target, sport, dport, 0, network_id, comment_tag, command_string,
       created_at, last_verified_at, is_active
FROM iptables_rules_v3;
DROP TABLE iptables_rules_v3;
CREATE INDEX idx_iptables_rules_network ON iptables_rules(network_id);
CREATE INDEX idx_iptables_rules_chain ON iptables_rules(table_name, chain_name);
CREATE INDEX idx_iptables_rules_type ON iptables_rules(rule_type);
CREATE INDEX idx_iptables_rules_active ON iptables_rules(is_active) WHERE is_active = 1;
CREATE INDEX idx_iptables_rules_policy ON iptables_rules(service_access_policy_id);
CREATE UNIQUE INDEX idx_iptables_rules_unique_active
    ON iptables_rules(network_id, rule_type, table_name, chain_name, protocol, source,
                      destination, in_interface, out_interface, target, sport, dport, dport_end)
    WHERE is_active = 1;

ALTER TABLE nftables_rules RENAME TO nftables_rules_v3;
CREATE TABLE nftables_rules (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    chain TEXT NOT NULL CHECK(chain LIKE 'MVM-%'),
    rule_type TEXT NOT NULL CHECK(rule_type IN (
        'masquerade', 'forward_in', 'forward_out', 'nocloudnet_input',
        'service_access_allow', 'routed_managed_drop', 'host_managed_drop'
    )),
    table_name TEXT NOT NULL CHECK(table_name IN ('filter', 'nat')),
    protocol TEXT NOT NULL CHECK(protocol IN ('tcp', 'udp', 'icmp', 'all')),
    source TEXT NOT NULL,
    destination TEXT NOT NULL,
    in_interface TEXT NOT NULL,
    out_interface TEXT NOT NULL,
    target TEXT NOT NULL,
    sport INTEGER NOT NULL,
    dport INTEGER NOT NULL,
    dport_end INTEGER NOT NULL DEFAULT 0,
    network_id TEXT NOT NULL,
    service_access_policy_id TEXT NULL,
    nft_handle INTEGER NULL,
    comment_tag TEXT NULL,
    command_string TEXT NULL,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP NOT NULL,
    last_verified_at TIMESTAMP NULL,
    is_active INTEGER DEFAULT 1 NOT NULL CHECK(is_active IN (0, 1)),
    FOREIGN KEY (network_id) REFERENCES networks(id) ON DELETE CASCADE,
    FOREIGN KEY (service_access_policy_id) REFERENCES service_access_policies(id) ON DELETE CASCADE
);
INSERT INTO nftables_rules (
    id, chain, rule_type, table_name, protocol, source, destination, in_interface,
    out_interface, target, sport, dport, dport_end, network_id, nft_handle, comment_tag,
    command_string, created_at, last_verified_at, is_active
)
SELECT id, chain, rule_type, table_name, protocol, source, destination, in_interface,
       out_interface, target, sport, dport, 0, network_id, nft_handle, comment_tag,
       command_string, created_at, last_verified_at, is_active
FROM nftables_rules_v3;
DROP TABLE nftables_rules_v3;
CREATE INDEX idx_nftables_rules_network ON nftables_rules(network_id);
CREATE INDEX idx_nftables_rules_chain ON nftables_rules(chain);
CREATE INDEX idx_nftables_rules_active ON nftables_rules(is_active) WHERE is_active = 1;
CREATE INDEX idx_nftables_rules_policy ON nftables_rules(service_access_policy_id);
CREATE UNIQUE INDEX idx_nftables_rules_unique_active
    ON nftables_rules(network_id, rule_type, chain, protocol, source, destination,
                      in_interface, out_interface, target, sport, dport, dport_end)
    WHERE is_active = 1;

PRAGMA foreign_keys = ON;
PRAGMA user_version = 4;
