-- Migration: 001_initial_schema
-- Version: 1
-- Description: Initial database schema with 10 tables
-- Created: 2026-04-02

-- IMAGES: OS image metadata
-- JSON mappings: internal_id -> type, filename -> path
CREATE TABLE images (
    id TEXT PRIMARY KEY,
    type TEXT NOT NULL,
    version TEXT NOT NULL,
    name TEXT NOT NULL,
    distro TEXT NULL,
    arch TEXT NOT NULL,
    path TEXT NOT NULL,
    fs_type TEXT NOT NULL,
    fs_uuid TEXT NULL,
    compressed_size INTEGER NULL,
    original_size INTEGER NOT NULL,
    compression_ratio REAL NULL,
    compressed_format TEXT NULL,
    minimum_rootfs_size_mib INTEGER NOT NULL,
    pulled_at TIMESTAMP NOT NULL,
    is_default INTEGER DEFAULT 0 NOT NULL,  -- Boolean: 0 or 1
    is_present INTEGER DEFAULT 0 NOT NULL,  -- Boolean: 0=file missing, 1=file exists
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP NOT NULL,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP NOT NULL,
    deleted_at TIMESTAMP NULL
);
CREATE INDEX idx_images_type ON images(type);
CREATE INDEX idx_images_name ON images(name);

-- KERNELS: Firecracker kernel metadata
-- JSON mappings: filename -> path, last_modified -> updated_at
CREATE TABLE kernels (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    base_name TEXT NOT NULL,
    version TEXT NOT NULL,
    arch TEXT NOT NULL,
    type TEXT NOT NULL,
    path TEXT NOT NULL,
    is_default INTEGER DEFAULT 0 NOT NULL,  -- Boolean: 0 or 1
    is_present INTEGER DEFAULT 0 NOT NULL,  -- Boolean: 0=file missing, 1=file exists
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP NOT NULL,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP NOT NULL,
    deleted_at TIMESTAMP NULL
);
CREATE INDEX idx_kernels_name ON kernels(name);
CREATE INDEX idx_kernels_version ON kernels(version);

-- BINARIES: Firecracker binary metadata
-- JSON mappings: package_version -> version
CREATE TABLE binaries (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    version TEXT NOT NULL,
    full_version TEXT NOT NULL,
    ci_version TEXT,
    path TEXT NOT NULL,
    is_default INTEGER DEFAULT 0 NOT NULL,  -- Boolean: 0 or 1
    is_present INTEGER DEFAULT 0 NOT NULL,  -- Boolean: 0=file missing, 1=file exists
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP NOT NULL,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP NOT NULL,
    deleted_at TIMESTAMP NULL
);
CREATE INDEX idx_binaries_name ON binaries(name);
CREATE INDEX idx_binaries_version ON binaries(version);

-- VOLUMES: Persistent data disks attachable to VMs
CREATE TABLE volumes (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL UNIQUE,
    size_bytes INTEGER NOT NULL,
    format TEXT NOT NULL DEFAULT 'raw',
    path TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'available',
    vm_id TEXT,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP NOT NULL,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP NOT NULL
);
CREATE INDEX idx_volumes_vm ON volumes(vm_id);
CREATE INDEX idx_volumes_name ON volumes(name);

-- NETWORKS: Named network definitions
CREATE TABLE networks (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL UNIQUE,
    subnet TEXT NOT NULL,
    bridge TEXT NOT NULL,
    ipv4_gateway TEXT NOT NULL,
    bridge_active INTEGER DEFAULT 0 NOT NULL,  -- Boolean: 0 or 1
    nat_gateways TEXT NULL,
    nat_enabled INTEGER DEFAULT 0 NOT NULL,  -- Boolean: 0 or 1
    is_default INTEGER DEFAULT 0 NOT NULL,  -- Boolean: 0 or 1
    is_present INTEGER DEFAULT 0 NOT NULL,  -- Boolean: 0=bridge missing, 1=bridge exists
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP NOT NULL,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP NOT NULL,
    deleted_at TIMESTAMP NULL
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
    leased_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP NOT NULL,
    expires_at TIMESTAMP NULL,
    UNIQUE(network_id, ipv4),
    FOREIGN KEY (network_id) REFERENCES networks(id) ON DELETE CASCADE
);
CREATE INDEX idx_leases_network ON network_leases(network_id);
CREATE INDEX idx_leases_vm ON network_leases(vm_id);
CREATE INDEX idx_leases_ipv4 ON network_leases(ipv4);

-- VM_STATES: VM runtime state
CREATE TABLE vm_instances (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL UNIQUE,
    status TEXT NOT NULL,
    pid INTEGER NOT NULL,
    process_start_time INTEGER NULL,
    ipv4 TEXT NOT NULL CHECK(ipv4 IS NULL OR ipv4 GLOB '[0-9]*.[0-9]*.[0-9]*.[0-9]*'),
    mac TEXT NOT NULL CHECK(mac IS NULL OR mac GLOB '[0-9A-Fa-f][0-9A-Fa-f]:[0-9A-Fa-f][0-9A-Fa-f]:[0-9A-Fa-f][0-9A-Fa-f]:[0-9A-Fa-f][0-9A-Fa-f]:[0-9A-Fa-f][0-9A-Fa-f]:[0-9A-Fa-f][0-9A-Fa-f]'),
    network_id TEXT NOT NULL,
    tap_device TEXT NOT NULL,
    image_id TEXT NOT NULL,
    kernel_id TEXT NOT NULL,
    binary_id TEXT NOT NULL,
    api_socket_path TEXT NOT NULL,
    relay_socket_path TEXT NULL,
    config_path TEXT NOT NULL,
    cloud_init_mode TEXT NOT NULL,
    nocloud_net_port INTEGER NULL,
    nocloud_net_pid INTEGER NULL,
    relay_pid INTEGER NULL,
    exit_code INTEGER NULL,
    log_path TEXT NULL,
    serial_output_path TEXT NULL,
    vcpu_count INTEGER NOT NULL,
    mem_size_mib INTEGER NOT NULL,
    disk_size_mib INTEGER NOT NULL,
    rootfs_path TEXT NOT NULL,
    rootfs_suffix TEXT NOT NULL,
    enable_pci INTEGER NOT NULL,  -- Boolean: 0 or 1
    lsm_flags TEXT NULL,
    enable_logging INTEGER NOT NULL,  -- Boolean: 0 or 1
    enable_metrics INTEGER NOT NULL,  -- Boolean: 0 or 1
    enable_console INTEGER NOT NULL,  -- Boolean: 0 or 1
    boot_args TEXT NULL,
    ssh_keys TEXT NULL,         -- JSON array of SSH key fingerprints
    ssh_user TEXT NULL,          -- SSH user configured for this VM
    volume_ids TEXT NULL,       -- JSON array of volume IDs
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP NOT NULL,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP NOT NULL,
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
    initialized INTEGER DEFAULT 0 NOT NULL,  -- Boolean: 0 or 1
    mvm_group_created INTEGER DEFAULT 0 NOT NULL,  -- Boolean: 0 or 1
    sudoers_configured INTEGER DEFAULT 0 NOT NULL,  -- Boolean: 0 or 1
    default_network_created INTEGER DEFAULT 0 NOT NULL,  -- Boolean: 0 or 1
    initialized_at TIMESTAMP NOT NULL,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP NOT NULL
);

-- HOST_STATE_CHANGES: Tracks host configuration changes for mvm host reset
CREATE TABLE host_state_changes (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id TEXT NOT NULL,
    init_timestamp TIMESTAMP NOT NULL,
    setting TEXT NOT NULL,
    mechanism TEXT NOT NULL,
    original_value TEXT NULL,
    applied_value TEXT NOT NULL,
    reverted INTEGER DEFAULT 0 NOT NULL,  -- Boolean: 0 or 1
    reverted_at TIMESTAMP NULL,
    revert_mechanism TEXT NULL,
    change_order INTEGER NOT NULL,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP NOT NULL,
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
    rule_type TEXT NOT NULL CHECK(rule_type IN ('masquerade', 'forward_in', 'forward_out', 'nocloudnet_input')),
    protocol TEXT NOT NULL CHECK(protocol IN ('tcp', 'udp', 'icmp', 'all')),  -- Default: 'all'
    source TEXT NOT NULL,                        -- Default: '0.0.0.0/0' (any source)
    destination TEXT NOT NULL,                   -- Default: '0.0.0.0/0' (any destination)
    in_interface TEXT NOT NULL,                  -- Input interface (-i), e.g., 'mvm-default'
    out_interface TEXT NOT NULL,                 -- Output interface (-o), e.g., 'eth0'
    target TEXT NOT NULL NOT NULL,               -- 'MASQUERADE', 'ACCEPT', 'DROP'
    sport INTEGER NOT NULL,                      -- Source port (optional)
    dport INTEGER NOT NULL,                      -- Destination port (optional)

    -- Resource Reference (rules always belong to a network)
    network_id TEXT NOT NULL,           -- FK to networks (CASCADE delete)

    -- Identification & Debugging
    comment_tag TEXT NULL,                   -- Full comment: 'mvm:{rule_type}:{network_name}:{context}'
    command_string TEXT NULL,                -- Full command for debugging

    -- Lifecycle
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP NOT NULL,
    last_verified_at TIMESTAMP NULL,         -- When last confirmed in iptables
    is_active INTEGER DEFAULT 1 NOT NULL CHECK(is_active IN (0, 1)),

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

-- SSH_KEYS: SSH key metadata and paths
CREATE TABLE ssh_keys (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL UNIQUE,
    fingerprint TEXT NOT NULL,
    algorithm TEXT NOT NULL,
    comment TEXT NOT NULL,
    private_key_path TEXT NULL,
    public_key_path TEXT NOT NULL,
    is_default INTEGER DEFAULT 0 NOT NULL,  -- Boolean: 0 or 1
    is_present INTEGER DEFAULT 0 NOT NULL,  -- Boolean: 0=file missing, 1=file exists
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP NOT NULL,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP NOT NULL
);

-- Indexes for efficient queries
CREATE INDEX idx_ssh_keys_name ON ssh_keys(name);
CREATE INDEX idx_ssh_keys_fingerprint ON ssh_keys(fingerprint);
CREATE INDEX idx_ssh_keys_is_default ON ssh_keys(is_default) WHERE is_default = 1;

-- USER_SETTINGS: Config overrides keyed by category
CREATE TABLE user_settings (
    category TEXT NOT NULL,
    key TEXT NOT NULL,
    value TEXT NOT NULL,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (category, key)
);

-- NFTABLES_RULES: Tracks every nftables rule created by mvmctl
CREATE TABLE IF NOT EXISTS nftables_rules (
    id INTEGER PRIMARY KEY AUTOINCREMENT,

    -- Rule Location
    chain TEXT NOT NULL CHECK(chain LIKE 'MVM-%'),       -- Only MVM-* chains (same as iptables)
    rule_type TEXT NOT NULL CHECK(rule_type IN ('masquerade', 'forward_in', 'forward_out', 'nocloudnet_input')),
    table_name TEXT NOT NULL CHECK(table_name IN ('filter')),

    -- Rule Parameters
    protocol TEXT NOT NULL CHECK(protocol IN ('tcp', 'udp', 'icmp', 'all')),
    source TEXT NOT NULL,
    destination TEXT NOT NULL,
    in_interface TEXT NOT NULL,
    out_interface TEXT NOT NULL,
    target TEXT NOT NULL,
    sport INTEGER NOT NULL,
    dport INTEGER NOT NULL,

    -- Resource Reference
    network_id TEXT NOT NULL REFERENCES networks(id) ON DELETE CASCADE,

    -- Identification & Debugging
    nft_handle INTEGER NULL,
    comment_tag TEXT NULL,
    command_string TEXT NULL,

    -- Lifecycle
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP NOT NULL,
    last_verified_at TIMESTAMP NULL,
    is_active INTEGER DEFAULT 1 NOT NULL CHECK(is_active IN (0, 1))
);

CREATE INDEX IF NOT EXISTS idx_nftables_rules_network ON nftables_rules(network_id);
CREATE INDEX IF NOT EXISTS idx_nftables_rules_chain ON nftables_rules(chain);
CREATE INDEX IF NOT EXISTS idx_nftables_rules_active ON nftables_rules(is_active) WHERE is_active = 1;
CREATE UNIQUE INDEX IF NOT EXISTS idx_nftables_rules_unique_active
    ON nftables_rules(network_id, rule_type, chain,
                      COALESCE(protocol, ''), COALESCE(source, ''),
                      COALESCE(destination, ''), COALESCE(in_interface, ''),
                      COALESCE(out_interface, ''), target,
                      COALESCE(sport, -1), COALESCE(dport, -1))
    WHERE is_active = 1;

-- Set schema version
PRAGMA user_version = 1;
