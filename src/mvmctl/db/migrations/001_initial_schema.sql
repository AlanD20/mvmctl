-- Migration: 001_initial_schema
-- Version: 1
-- Description: Initial database schema with 10 tables
-- Created: 2026-04-02

-- IMAGES: OS image metadata
-- JSON mappings: internal_id -> os_slug, filename -> path
CREATE TABLE images (
    id TEXT PRIMARY KEY,
    os_slug TEXT NOT NULL UNIQUE,
    os_name TEXT,
    arch TEXT NOT NULL,
    path TEXT NOT NULL,
    fs_type TEXT,
    fs_uuid TEXT,
    compressed_size INTEGER,
    original_size INTEGER,
    compression_ratio REAL,
    compressed_format TEXT,
    minimum_rootfs_size_mib INTEGER NOT NULL,
    pulled_at TIMESTAMP,
    is_default INTEGER DEFAULT 0,  -- Boolean: 0 or 1
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX idx_images_os_slug ON images(os_slug);
CREATE INDEX idx_images_name ON images(os_name);

-- KERNELS: Firecracker kernel metadata
-- JSON mappings: filename -> path, last_modified -> updated_at
CREATE TABLE kernels (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    base_name TEXT,
    version TEXT NOT NULL,
    arch TEXT NOT NULL,
    type TEXT,
    path TEXT NOT NULL,
    is_default INTEGER DEFAULT 0,  -- Boolean: 0 or 1
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX idx_kernels_name ON kernels(name);
CREATE INDEX idx_kernels_version ON kernels(version);

-- BINARIES: Firecracker binary metadata
-- JSON mappings: package_version -> version
CREATE TABLE binaries (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    version TEXT NOT NULL,
    full_version TEXT,
    ci_version TEXT,
    path TEXT NOT NULL,
    is_default INTEGER DEFAULT 0,  -- Boolean: 0 or 1
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX idx_binaries_name ON binaries(name);
CREATE INDEX idx_binaries_version ON binaries(version);

-- NETWORKS: Named network definitions
-- JSON mappings: cidr -> subnet, gateway -> ipv4_gateway
CREATE TABLE networks (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL UNIQUE,
    subnet TEXT NOT NULL,
    bridge TEXT NOT NULL,
    ipv4_gateway TEXT NOT NULL,
    bridge_active INTEGER DEFAULT 0,  -- Boolean: 0 or 1
    nat_gateways TEXT NULL,
    nat_enabled INTEGER DEFAULT 0,  -- Boolean: 0 or 1
    is_default INTEGER DEFAULT 0,  -- Boolean: 0 or 1
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX idx_networks_name ON networks(name);

-- NETWORK_LEASES: IP allocation tracking
-- JSON mappings: vm_name -> vm_id (stores ID hash), ip -> ipv4
-- expires_at is NULL by default: leases are valid for VM's entire lifecycle
CREATE TABLE network_leases (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    network_id TEXT NOT NULL,
    ipv4 TEXT NOT NULL CHECK(ipv4 GLOB '[0-9]*.[0-9]*.[0-9]*.[0-9]*'),
    vm_id TEXT NULL, -- cannot create FK, but still references an active VM!
    leased_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    expires_at TIMESTAMP NULL,
    UNIQUE(network_id, ipv4),
    FOREIGN KEY (network_id) REFERENCES networks(id) ON DELETE CASCADE
);
CREATE INDEX idx_leases_network ON network_leases(network_id);
CREATE INDEX idx_leases_vm ON network_leases(vm_id);
CREATE INDEX idx_leases_ipv4 ON network_leases(ipv4);

-- VM_STATES: VM runtime state
-- JSON mappings: socket_path -> api_socket_path, ip -> ipv4, network_name -> network_id
CREATE TABLE vm_instances (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL UNIQUE,
    status TEXT NOT NULL,
    pid INTEGER,
    ipv4 TEXT CHECK(ipv4 IS NULL OR ipv4 GLOB '[0-9]*.[0-9]*.[0-9]*.[0-9]*'),
    mac TEXT CHECK(mac IS NULL OR mac GLOB '[0-9A-Fa-f][0-9A-Fa-f]:[0-9A-Fa-f][0-9A-Fa-f]:[0-9A-Fa-f][0-9A-Fa-f]:[0-9A-Fa-f][0-9A-Fa-f]:[0-9A-Fa-f][0-9A-Fa-f]:[0-9A-Fa-f][0-9A-Fa-f]'),
    network_id TEXT,
    tap_device TEXT,
    image_id TEXT,
    kernel_id TEXT,
    binary_id TEXT,
    api_socket_path TEXT,
    console_socket_path TEXT,
    config_path TEXT,
    cloud_init_mode TEXT,
    nocloud_net_port INTEGER,
    nocloud_server_pid INTEGER,
    console_relay_pid INTEGER,
    exit_code INTEGER,
    vcpu_count INTEGER,
    mem_size_mib INTEGER,
    disk_size_mib INTEGER,
    rootfs_path TEXT,
    rootfs_suffix TEXT,
    enable_api_socket INTEGER,  -- Boolean: 0 or 1
    enable_pci INTEGER,  -- Boolean: 0 or 1
    lsm_flags TEXT,
    enable_logging INTEGER,  -- Boolean: 0 or 1
    enable_metrics INTEGER,  -- Boolean: 0 or 1
    enable_console INTEGER,  -- Boolean: 0 or 1
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (network_id) REFERENCES networks(id) ON DELETE RESTRICT,
    FOREIGN KEY (image_id) REFERENCES images(id) ON DELETE RESTRICT,
    FOREIGN KEY (kernel_id) REFERENCES kernels(id) ON DELETE RESTRICT,
    FOREIGN KEY (binary_id) REFERENCES binaries(id) ON DELETE RESTRICT
);
CREATE INDEX idx_vm_instances_name ON vm_instances(name);
CREATE INDEX idx_vm_instances_status ON vm_instances(status);

-- HOST_STATE: Host initialization state (singleton, always id=1)
CREATE TABLE host_state (
    id INTEGER PRIMARY KEY,
    initialized INTEGER DEFAULT 0,  -- Boolean: 0 or 1
    mvm_group_created INTEGER DEFAULT 0,  -- Boolean: 0 or 1
    sudoers_configured INTEGER DEFAULT 0,  -- Boolean: 0 or 1
    default_network_created INTEGER DEFAULT 0,  -- Boolean: 0 or 1
    initialized_at TIMESTAMP,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

-- HOST_STATE_CHANGES: Tracks host configuration changes for mvm host reset
CREATE TABLE host_state_changes (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id TEXT NOT NULL,
    init_timestamp TIMESTAMP NOT NULL,
    setting TEXT NOT NULL,
    mechanism TEXT NOT NULL,
    original_value TEXT,
    applied_value TEXT NOT NULL,
    reverted INTEGER DEFAULT 0,  -- Boolean: 0 or 1
    reverted_at TIMESTAMP,
    revert_mechanism TEXT,
    change_order INTEGER NOT NULL,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(session_id, change_order)
);
CREATE INDEX idx_host_changes_session ON host_state_changes(session_id);
CREATE INDEX idx_host_changes_setting ON host_state_changes(setting);
CREATE INDEX idx_host_changes_reverted ON host_state_changes(reverted);

-- IPTABLES_RULES: Tracks every iptables rule created by mvmctl
-- Enables reliable cleanup and synchronization
CREATE TABLE iptables_rules (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    
    -- Rule Location
    table_name TEXT NOT NULL CHECK(table_name IN ('nat', 'filter')),
    chain_name TEXT NOT NULL CHECK(chain_name LIKE 'MVM-%'),  -- Only MVM-* chains
    
    -- Rule Parameters (explicit columns for precise matching)
    rule_type TEXT NOT NULL CHECK(rule_type IN ('masquerade', 'forward_in', 'forward_out', 'nocloud_input')),
    protocol TEXT CHECK(protocol IN ('tcp', 'udp', 'icmp', 'all')),  -- NULL = any protocol
    source TEXT,                        -- Source CIDR or IP (e.g., '10.0.0.0/24', '0.0.0.0/0')
    destination TEXT,                   -- Dest CIDR or IP
    in_interface TEXT,                  -- Input interface (-i), e.g., 'mvm-default'
    out_interface TEXT,                 -- Output interface (-o), e.g., 'eth0'
    target TEXT NOT NULL,               -- 'MASQUERADE', 'ACCEPT', 'DROP'
    sport INTEGER,                      -- Source port (optional)
    dport INTEGER,                      -- Destination port (optional)
    
    -- Resource Reference (rules always belong to a network)
    network_id TEXT NOT NULL,           -- FK to networks (CASCADE delete)
    
    -- Identification & Debugging
    comment_tag TEXT,                   -- Full comment: 'mvm:{rule_type}:{network_name}:{context}'
    command_string TEXT,                -- Full command for debugging
    
    -- Lifecycle
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    last_verified_at TIMESTAMP,         -- When last confirmed in iptables
    is_active INTEGER DEFAULT 1 CHECK(is_active IN (0, 1)),
    
    -- Constraints
    FOREIGN KEY (network_id) REFERENCES networks(id) ON DELETE CASCADE
);

-- Indexes for efficient queries
CREATE INDEX idx_iptables_rules_network ON iptables_rules(network_id);
CREATE INDEX idx_iptables_rules_chain ON iptables_rules(table_name, chain_name);
CREATE INDEX idx_iptables_rules_type ON iptables_rules(rule_type);
CREATE INDEX idx_iptables_rules_active ON iptables_rules(is_active) WHERE is_active = 1;
CREATE INDEX idx_iptables_rules_interfaces ON iptables_rules(in_interface, out_interface) 
    WHERE in_interface IS NOT NULL OR out_interface IS NOT NULL;
CREATE INDEX idx_iptables_rules_network_type ON iptables_rules(network_id, rule_type);

-- Prevent duplicate active rules for same network+spec combination
CREATE UNIQUE INDEX idx_iptables_rules_unique_active 
    ON iptables_rules(network_id, rule_type, table_name, chain_name, 
                      COALESCE(protocol, ''), COALESCE(source, ''), 
                      COALESCE(destination, ''), COALESCE(in_interface, ''), 
                      COALESCE(out_interface, ''), target, 
                      COALESCE(sport, -1), COALESCE(dport, -1))
    WHERE is_active = 1;

-- Set schema version
PRAGMA user_version = 1;
