-- Fresh v0.3.0 baseline. Migration identity and version are committed by the runner.
CREATE TABLE db_migrations (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    version INTEGER NOT NULL UNIQUE,
    name TEXT NOT NULL,
    applied_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    checksum TEXT,
    snapshot_path TEXT
);

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
    is_imported INTEGER DEFAULT 0 NOT NULL,  -- Boolean: 0=pulled from remote, 1=imported from local
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
    deleted_at TIMESTAMP NULL,
    features TEXT NULL
);
CREATE INDEX idx_kernels_name ON kernels(name);
CREATE INDEX idx_kernels_version ON kernels(version);

-- BINARIES: Firecracker binary metadata
-- JSON mappings: package_version -> version
CREATE TABLE binaries (
    id TEXT PRIMARY KEY,
    type TEXT NOT NULL CHECK(type IN ('firecracker', 'jailer')),
    architecture TEXT NOT NULL CHECK(architecture IN ('x86_64', 'aarch64')),
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
CREATE INDEX idx_binaries_type ON binaries(type);
CREATE INDEX idx_binaries_version ON binaries(architecture, version);
CREATE UNIQUE INDEX idx_binaries_active_release_member
    ON binaries(type, architecture, version)
    WHERE deleted_at IS NULL;
CREATE UNIQUE INDEX idx_binaries_active_default
    ON binaries(type, architecture)
    WHERE is_default = 1 AND deleted_at IS NULL;

-- VOLUMES: Persistent data disks attachable to VMs
CREATE TABLE volumes (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL UNIQUE,
    size_bytes INTEGER NOT NULL,
    format TEXT NOT NULL DEFAULT 'raw',
    is_read_only INTEGER DEFAULT 0 NOT NULL,  -- Boolean: 0 or 1
    is_shareable INTEGER DEFAULT 0 NOT NULL,  -- Boolean: 0 or 1
    cache_type TEXT NOT NULL,
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
    network_placement TEXT NOT NULL DEFAULT 'managed' CHECK(network_placement IN ('managed', 'networkless')),
    ipv4 TEXT NOT NULL CHECK(
        (network_placement = 'managed' AND ipv4 GLOB '[0-9]*.[0-9]*.[0-9]*.[0-9]*') OR
        (network_placement = 'networkless' AND ipv4 = '')
    ),
    mac TEXT NOT NULL CHECK(
        (network_placement = 'managed' AND mac GLOB '[0-9A-Fa-f][0-9A-Fa-f]:[0-9A-Fa-f][0-9A-Fa-f]:[0-9A-Fa-f][0-9A-Fa-f]:[0-9A-Fa-f][0-9A-Fa-f]:[0-9A-Fa-f][0-9A-Fa-f]:[0-9A-Fa-f][0-9A-Fa-f]') OR
        (network_placement = 'networkless' AND mac = '')
    ),
    network_id TEXT CHECK(
        (network_placement = 'managed' AND network_id IS NOT NULL AND network_id != '') OR
        (network_placement = 'networkless' AND network_id IS NULL)
    ),
    tap_device TEXT NOT NULL CHECK(network_placement = 'managed' OR tap_device = ''),
    image_id TEXT NOT NULL,
    kernel_id TEXT NOT NULL,
    binary_id TEXT NOT NULL,
    relay_socket_path TEXT NULL,
    config_path TEXT NOT NULL,
    cloud_init_mode TEXT NOT NULL CHECK(network_placement = 'managed' OR cloud_init_mode != 'net'),
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
    pci_enabled INTEGER NOT NULL,  -- Boolean: 0 or 1
    nested_virt INTEGER NOT NULL DEFAULT 0,  -- Boolean: 0 or 1
    cpu_config TEXT NULL,  -- JSON: merged CPU template config
    lsm_flags TEXT NOT NULL DEFAULT '',
    enable_logging INTEGER NOT NULL,  -- Boolean: 0 or 1
    enable_metrics INTEGER NOT NULL,  -- Boolean: 0 or 1
    enable_console INTEGER NOT NULL,  -- Boolean: 0 or 1
    boot_args TEXT NOT NULL DEFAULT '',
    ssh_keys TEXT NULL,         -- JSON array of SSH key fingerprints
    ssh_user TEXT NULL,          -- SSH user configured for this VM
    volume_ids TEXT NULL,       -- JSON array of volume IDs
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP NOT NULL,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP NOT NULL,
    jailer_binary_id TEXT NOT NULL DEFAULT '',
    cgroup_limits TEXT NOT NULL DEFAULT '{"policy_version":0,"cpu_quota_micros":100000,"cpu_period_micros":100000,"cpu_weight":100,"memory_high_bytes":671088640,"memory_max_bytes":671088640,"swap_max_bytes":0,"pids_max":256}',
    FOREIGN KEY (network_id) REFERENCES networks(id) ON DELETE RESTRICT,
    FOREIGN KEY (image_id) REFERENCES images(id) ON DELETE RESTRICT,
    FOREIGN KEY (kernel_id) REFERENCES kernels(id) ON DELETE RESTRICT,
    FOREIGN KEY (binary_id) REFERENCES binaries(id) ON DELETE RESTRICT
);
CREATE INDEX idx_vm_instances_name ON vm_instances(name);
CREATE INDEX idx_vm_instances_status ON vm_instances(status);
CREATE INDEX idx_vm_instances_jailer_binary ON vm_instances(jailer_binary_id);

-- HOST_STATE: Host initialization state (singleton, always id=1)
CREATE TABLE host_state (
    id INTEGER PRIMARY KEY,
    initialized INTEGER DEFAULT 0 NOT NULL,  -- Boolean: 0 or 1
    mvm_group_created INTEGER DEFAULT 0 NOT NULL,  -- Boolean: 0 or 1
    sudoers_configured INTEGER DEFAULT 0 NOT NULL,  -- Boolean: 0 or 1
    default_network_created INTEGER DEFAULT 0 NOT NULL,  -- Boolean: 0 or 1
    initialized_at TIMESTAMP NOT NULL,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP NOT NULL,

    -- Capacity detection (populated by host info --refresh or host init)
    hostname TEXT,
    cpu_model TEXT,
    cpu_vendor TEXT,
    cpu_cores INTEGER,
    cpu_architecture TEXT,
    numa_nodes INTEGER DEFAULT 1,
    memory_total_mib INTEGER,
    storage_total_bytes INTEGER,
    kernel_version TEXT,
    os_release TEXT,
    pid_max INTEGER,
    fd_max INTEGER,
    conntrack_max INTEGER,
    tap_devices_max INTEGER,
    ip_local_port_range TEXT,
    detected_at TIMESTAMP,

    -- Virtualization & system detection
    cpu_has_vmx INTEGER,
    cpu_hypervisor INTEGER,
    nested_virt_available INTEGER,
    ept_available INTEGER,
    hugepage_count_2mb INTEGER,
    ksm_disabled INTEGER,
    cgroup_version INTEGER,
    swap_total_mib INTEGER,
    kernel_minimum_met INTEGER
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

-- VSOCK_CONFIG: Per-VM vsock device and agent configuration
CREATE TABLE vm_vsock_config (
    id TEXT PRIMARY KEY,
    vm_id TEXT NOT NULL UNIQUE,
    guest_cid INTEGER NOT NULL,
    uds_path TEXT NOT NULL,
    port INTEGER NOT NULL,
    token TEXT NOT NULL,
    agent_version TEXT NOT NULL DEFAULT '',
    upgrading INTEGER NOT NULL DEFAULT 0,
    upgrade_started_at TIMESTAMP NULL,
    FOREIGN KEY (vm_id) REFERENCES vm_instances(id) ON DELETE CASCADE
);
CREATE INDEX idx_vsock_config_vm ON vm_vsock_config(vm_id);
CREATE INDEX idx_vsock_config_cid ON vm_vsock_config(guest_cid);

-- SNAPSHOTS: Managed Firecracker snapshot metadata
CREATE TABLE snapshots (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    source_vm_id TEXT NOT NULL,
    source_vm_name TEXT NOT NULL,
    snapshot_dir TEXT NOT NULL,
    memory_file TEXT NOT NULL,
    state_file TEXT NOT NULL,
    rootfs_file TEXT NOT NULL,
    kernel_id TEXT NOT NULL,
    network_id TEXT NOT NULL,
    binary_id TEXT NOT NULL,
    vcpu_count INTEGER NOT NULL,
    mem_size_mib INTEGER NOT NULL,
    disk_size_mib INTEGER NOT NULL,
    ssh_keys TEXT NULL,
    ssh_user TEXT NULL,
    image_id TEXT NOT NULL,
    extra_config TEXT NULL,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP NOT NULL,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP NOT NULL
);
CREATE INDEX idx_snapshots_name ON snapshots(name);
CREATE INDEX idx_snapshots_kernel ON snapshots(kernel_id);
CREATE INDEX idx_snapshots_network ON snapshots(network_id);
CREATE INDEX idx_snapshots_binary ON snapshots(binary_id);

CREATE TABLE network_traffic_policies (
    id TEXT PRIMARY KEY CHECK(length(id) = 64 AND id NOT GLOB '*[^0-9a-f]*'),
    source_network_id TEXT NOT NULL,
    destination_vm_id TEXT NOT NULL,
    protocol TEXT NOT NULL CHECK(protocol IN ('tcp', 'udp')),
    destination_port_start INTEGER NOT NULL CHECK(destination_port_start BETWEEN 1 AND 65535),
    destination_port_end INTEGER NOT NULL CHECK(destination_port_end BETWEEN destination_port_start AND 65535),
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP NOT NULL,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP NOT NULL,
    claim_id TEXT NOT NULL DEFAULT '',
    incarnation TEXT NOT NULL DEFAULT '',
    FOREIGN KEY (source_network_id) REFERENCES networks(id) ON DELETE CASCADE,
    FOREIGN KEY (destination_vm_id) REFERENCES vm_instances(id) ON DELETE CASCADE,
    UNIQUE(source_network_id, destination_vm_id, protocol, destination_port_start, destination_port_end)
);
CREATE INDEX idx_network_traffic_source ON network_traffic_policies(source_network_id);
CREATE INDEX idx_network_traffic_destination ON network_traffic_policies(destination_vm_id);

CREATE TABLE vm_traffic_policies (
    id TEXT PRIMARY KEY CHECK(length(id) = 64 AND id NOT GLOB '*[^0-9a-f]*'),
    source_vm_id TEXT NOT NULL,
    destination_vm_id TEXT NOT NULL,
    protocol TEXT NOT NULL CHECK(protocol IN ('tcp', 'udp')),
    destination_port_start INTEGER NOT NULL CHECK(destination_port_start BETWEEN 1 AND 65535),
    destination_port_end INTEGER NOT NULL CHECK(destination_port_end BETWEEN destination_port_start AND 65535),
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP NOT NULL,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP NOT NULL,
    claim_id TEXT NOT NULL DEFAULT '',
    incarnation TEXT NOT NULL DEFAULT '',
    FOREIGN KEY (source_vm_id) REFERENCES vm_instances(id) ON DELETE CASCADE,
    FOREIGN KEY (destination_vm_id) REFERENCES vm_instances(id) ON DELETE CASCADE,
    CHECK(source_vm_id <> destination_vm_id),
    UNIQUE(source_vm_id, destination_vm_id, protocol, destination_port_start, destination_port_end)
);
CREATE INDEX idx_vm_traffic_source ON vm_traffic_policies(source_vm_id);
CREATE INDEX idx_vm_traffic_destination ON vm_traffic_policies(destination_vm_id);

CREATE TABLE exec_policies (
    id TEXT PRIMARY KEY CHECK(length(id) = 64 AND id NOT GLOB '*[^0-9a-f]*'),
    source_vm_id TEXT NOT NULL,
    destination_vm_id TEXT NOT NULL,
    target_user TEXT NOT NULL CHECK(
        length(target_user) BETWEEN 1 AND 32
        AND target_user <> 'root'
        AND substr(target_user, 1, 1) GLOB '[a-z_]'
        AND target_user NOT GLOB '*[^a-z0-9_-]*'
    ),
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP NOT NULL,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP NOT NULL,
    claim_id TEXT NOT NULL DEFAULT '',
    incarnation TEXT NOT NULL DEFAULT '',
    FOREIGN KEY (source_vm_id) REFERENCES vm_instances(id) ON DELETE CASCADE,
    FOREIGN KEY (destination_vm_id) REFERENCES vm_instances(id) ON DELETE CASCADE,
    CHECK(source_vm_id <> destination_vm_id),
    UNIQUE(source_vm_id, destination_vm_id, target_user)
);
CREATE INDEX idx_exec_policy_source ON exec_policies(source_vm_id);
CREATE INDEX idx_exec_policy_destination ON exec_policies(destination_vm_id);

-- Host traffic policy projections.
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


-- Root reservation identity; no local floating-IP ownership projection.
CREATE TABLE vm_floating_ip_traffic_policies (
    id TEXT PRIMARY KEY CHECK(length(id) = 64 AND id NOT GLOB '*[^0-9a-f]*'),
    source_vm_id TEXT NOT NULL,
    destination_floating_ip_id TEXT NOT NULL CHECK(length(destination_floating_ip_id) = 32 AND destination_floating_ip_id NOT GLOB '*[^0-9a-f]*'),
    protocol TEXT NOT NULL CHECK(protocol IN ('tcp', 'udp')),
    destination_port_start INTEGER NOT NULL CHECK(destination_port_start BETWEEN 1 AND 65535),
    destination_port_end INTEGER NOT NULL CHECK(destination_port_end BETWEEN destination_port_start AND 65535),
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP NOT NULL,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP NOT NULL,
    claim_id TEXT NOT NULL DEFAULT '',
    incarnation TEXT NOT NULL DEFAULT '',
    FOREIGN KEY (source_vm_id) REFERENCES vm_instances(id) ON DELETE CASCADE,
    UNIQUE(source_vm_id, destination_floating_ip_id, protocol, destination_port_start, destination_port_end)
);
CREATE INDEX idx_vm_floating_ip_traffic_source ON vm_floating_ip_traffic_policies(source_vm_id);
CREATE INDEX idx_vm_floating_ip_traffic_destination ON vm_floating_ip_traffic_policies(destination_floating_ip_id);


-- Root reservation identity; no local floating-IP ownership projection.
CREATE TABLE network_floating_ip_traffic_policies (
    id TEXT PRIMARY KEY CHECK(length(id) = 64 AND id NOT GLOB '*[^0-9a-f]*'),
    source_network_id TEXT NOT NULL,
    destination_floating_ip_id TEXT NOT NULL CHECK(length(destination_floating_ip_id) = 32 AND destination_floating_ip_id NOT GLOB '*[^0-9a-f]*'),
    protocol TEXT NOT NULL CHECK(protocol IN ('tcp', 'udp')),
    destination_port_start INTEGER NOT NULL CHECK(destination_port_start BETWEEN 1 AND 65535),
    destination_port_end INTEGER NOT NULL CHECK(destination_port_end BETWEEN destination_port_start AND 65535),
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP NOT NULL,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP NOT NULL,
    claim_id TEXT NOT NULL DEFAULT '',
    incarnation TEXT NOT NULL DEFAULT '',
    FOREIGN KEY (source_network_id) REFERENCES networks(id) ON DELETE CASCADE,
    UNIQUE(source_network_id, destination_floating_ip_id, protocol, destination_port_start, destination_port_end)
);
CREATE INDEX idx_network_floating_ip_traffic_source ON network_floating_ip_traffic_policies(source_network_id);
CREATE INDEX idx_network_floating_ip_traffic_destination ON network_floating_ip_traffic_policies(destination_floating_ip_id);
