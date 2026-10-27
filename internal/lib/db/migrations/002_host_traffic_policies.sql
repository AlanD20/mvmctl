-- Host grants are new projections. Preserve the published baseline and all existing entities.
CREATE TABLE vm_host_traffic_policies (
    id TEXT PRIMARY KEY CHECK(length(id) = 64 AND id NOT GLOB '*[^0-9a-f]*'),
    source_vm_id TEXT NOT NULL,
    protocol TEXT NOT NULL CHECK(protocol IN ('tcp', 'udp')),
    destination_port_start INTEGER NOT NULL CHECK(destination_port_start BETWEEN 1 AND 65535),
    destination_port_end INTEGER NOT NULL CHECK(destination_port_end BETWEEN destination_port_start AND 65535),
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP NOT NULL,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP NOT NULL,
    claim_id TEXT NOT NULL DEFAULT '',
    incarnation TEXT NOT NULL DEFAULT '',
    FOREIGN KEY (source_vm_id) REFERENCES vm_instances(id) ON DELETE CASCADE,
    UNIQUE(source_vm_id, protocol, destination_port_start, destination_port_end)
);
CREATE INDEX idx_vm_host_traffic_source ON vm_host_traffic_policies(source_vm_id);

CREATE TABLE network_host_traffic_policies (
    id TEXT PRIMARY KEY CHECK(length(id) = 64 AND id NOT GLOB '*[^0-9a-f]*'),
    source_network_id TEXT NOT NULL,
    protocol TEXT NOT NULL CHECK(protocol IN ('tcp', 'udp')),
    destination_port_start INTEGER NOT NULL CHECK(destination_port_start BETWEEN 1 AND 65535),
    destination_port_end INTEGER NOT NULL CHECK(destination_port_end BETWEEN destination_port_start AND 65535),
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP NOT NULL,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP NOT NULL,
    claim_id TEXT NOT NULL DEFAULT '',
    incarnation TEXT NOT NULL DEFAULT '',
    FOREIGN KEY (source_network_id) REFERENCES networks(id) ON DELETE CASCADE,
    UNIQUE(source_network_id, protocol, destination_port_start, destination_port_end)
);
CREATE INDEX idx_network_host_traffic_source ON network_host_traffic_policies(source_network_id);
