# mvmctl/utils/ — Shared Helpers

**Scope:** Pure, domain-agnostic utilities; no business logic, no Firecracker knowledge
**Status:** Pre-production project — refactoring MUST NOT create legacy migration logic.
**Rule:** Never import from `core/`, `api/`, or `cli/`; zero side effects unless explicitly called

## STRUCTURE

```
src/mvmctl/utils/
├── console.py           # Plain-text console utilities — Rich shim for non-TTY environments
├── fs.py                # Cache/config path resolution; SUDO_USER-aware home; test path helpers
├── http.py              # Resumable download with SHA256 verify (uses Typer for interactive confirm)
├── process.py           # Subprocess wrappers; sudo credential cache
├── validation.py        # Entity name, network, filesystem, and security validators
├── audit.py             # Append-only audit log → ~/.cache/mvmctl/audit.log
├── guestfs.py           # ALL libguestfs operations (OptimizedGuestfs, check_libguestfs, extract_partition_with_guestfs)
├── template.py          # String template rendering
├── time.py              # Human-readable relative time formatting ("5 minutes ago")
├── yaml.py              # Typed field extraction helpers for YAML-parsed dictionaries
├── network.py           # Network utilities: MAC, TAP, IP, iptables, bridges
├── id_lookup.py         # Short-ID prefix matching utilities
├── progress.py          # ASCII download progress bars
├── disk_size.py         # Disk size parsing/formatting
├── resize.py            # Image resize via qemu-img
├── full_hash.py         # SHA256 hash generation for assets
├── debug_state.py       # Global debug mode state management
├── error_handler.py     # Centralized error formatting and handling helpers
└── partition_detection.py  # Root partition detection with weighted heuristics
```

**Package `__all__`:** Only `console`, `fs`, `http` are re-exported from `utils/__init__.py`.
Other modules are used throughout the codebase but not package-exported — import them directly.

## MODULE DETAILS

### console.py
Plain-text console utilities for non-Rich environments.
- `_strip_markup()` — Remove Rich markup tags
- `_PlainConsole` class — Drop-in Rich Console shim
- `print_info(msg)`, `print_warning(msg)`, `print_error(msg)`, `print_success(msg)` — Formatted output
- `print_table(headers, rows)` — Plain-text column-aligned tables
- `print_section_header()`, `print_inspect_header()` — Headers
- `print_key_value()` — Key-value pair formatting
- `format_timestamp()` — ISO timestamp formatting
- `get_state_marker()`, `get_combined_marker()` — Status markers
- **Only** CLI layer and `cli/`-adjacent code should call these; `core/` must raise exceptions instead

### fs.py
Cache/config path resolution with SUDO_USER awareness.
- `get_cache_dir()` → `Path` — respects `MVM_CACHE_DIR` env, falls back to `~/.cache/mvmctl/`
- `get_config_dir()` → `Path` — respects `MVM_CONFIG_DIR`, falls back to `~/.config/mvmctl/`
- `get_config_file()`, `get_mvm_db_path()` — Specific file paths
- `get_temp_dir()` — Temporary directory
- `get_vms_dir()`, `get_vm_dir()`, `get_vm_dir_by_hash()` — VM directories
- `get_images_dir()`, `get_kernels_dir()`, `get_bin_dir()`, `get_logs_dir()` — Asset dirs
- `get_keys_dir()`, `get_keys_config_dir()` — SSH key directories
- `get_assets_dir()` — Bundled package assets
- `get_real_user_ids()` — (uid, gid) for chown operations
- `write_pid_file()`, `read_pid_file()` — PID file management
- `write_exit_code()` — Exit code persistence
- `secure_mkdir()` — Symlink-attack resistant directory creation
- `chown_to_real_user()` — Recursive chown to invoking user
- `is_file_missing()`, `get_file_size()` — File utilities
- `SUDO_USER` home resolution — when run via `sudo`, resolves to the invoking user's home (not root's)

### http.py
Resumable HTTP downloads with SHA256 verification.
- `HttpDownload.with_download(url, dest, timeout, progress_callback)` — pure transport; atomic `os.replace`; retries via `@_with_retry`
- `HttpDownload.download_file(url, dest, sha256, progress_bar)` — orchestration wrapper with SHA256 verify + ASCII progress bar
- `HttpDownload._parse_content_length()` — Content-Length parsing
- `urlopen()` — HTTP opener with keep-alive
- `_with_retry()` — Retry decorator with exponential backoff
- Sets `User-Agent: mvmctl/{version}`
- Uses Typer for interactive confirmation when no checksum provided
- Used by `image.py`, `kernel.py`, `binary_manager.py` for all asset downloads

### process.py
Subprocess wrappers with privilege management.
- `run_cmd(cmd, ...)` → `CompletedProcess` — list form only; raises `ProcessError` on failure
- `stream_cmd(cmd, ...)` → yields stdout lines — for long-running builds (kernel make)
- `_is_sudo_cached()`, `_validate_sudo_credentials()` — Sudo credential cache
- `privileged_cmd()` — Prepend sudo if not root
- `require_mvm_group_membership()` — Validate mvm group membership
- `is_process_running()` — Check if PID is active
- Always captures stderr and includes in `ProcessError.stderr`

### validation.py
Input validation for entity names, network, and security.
- `validate_entity_name(name)` — VM/network/key names: alphanumeric + hyphen, 1–63 chars
- `validate_boot_arg_component(value)` — safe kernel cmdline component
- `is_ip_address(value)` → `bool`
- `validate_fs_uuid()` — Filesystem UUID format validation
- `validate_fs_type()` — Filesystem type validation
- `validate_interface_name()` — Network interface security validation
- `validate_bridge_name()` — Bridge name validation
- `validate_subnet()` — CIDR validation
- `validate_ipv4_address()` — IPv4 validation
- `validate_nat_gateways()` — NAT gateway interface list validation
- `sanitize_metadata_string()` — Metadata sanitization

### audit.py
- `log_audit(action, details)` — appends JSON line to `$MVM_CACHE_DIR/audit.log`
- Called from `cli/` layer (`cli/host.py`, `cli/vm.py`) — NOT from `api/`

### guestfs.py
- `OptimizedGuestfs` — libguestfs wrapper with connection pooling
- `optimized_guestfs()` — Context manager function
- `check_libguestfs()` — availability check
- `extract_partition_with_guestfs(...)` — partition extraction from VHD
- `_find_largest_linux_fs()` — Heuristic root detection
- `_get_fs_size()` — Filesystem size calculation
- **ALL** libguestfs operations belong here — never in `core/`

### template.py
- `render_template()` — Basic string template with variable substitution
- `render_optional_template()` — Nullable version

### time.py
- `human_readable_time()` — Convert ISO timestamp to relative time ("5 minutes ago")

### yaml.py
Typed field extraction helpers for YAML-parsed dictionaries.
- `require_str()` — Required string field
- `optional_str()` — Optional string field
- `optional_int()` — Optional integer field
- `require_str_list()` — Required list of strings
- `parse_set_val_list()` — Option/value pair parsing

### network.py
Network utilities: MAC, TAP, IP, iptables, bridges.
- `subnet_mask_from_subnet()`, `prefix_len_from_subnet()` — Subnet math
- `ipv4_gateway_for_subnet()` — Gateway calculation
- `bridge_name_for()` — Bridge naming
- `generate_mac()` — MAC address generation
- `generate_tap_name()` — TAP device naming
- `list_network_interfaces()` — Physical interface listing
- `get_default_interface()` — Default route interface
- `bridge_exists()`, `tap_exists()` — Existence checks
- `chain_exists()` — iptables chain check
- `list_tuntap_devices()`, `list_bridges()` — Device enumeration
- `allocate_ip()` — IP allocation from subnet
- `get_iptables_rules_for_bridge()` — Rule listing
- `validate_network_interface()` — Interface validation
- `_run_ip_batch()` — Batch ip command execution
- `_bridge_has_ip()` — Bridge IP check
- `_iptables_rule_exists()`, `_ensure_iptables_rule()` — Rule management
- `_build_iptables_restore_input()`, `_apply_iptables_rules_batch()` — Batch iptables
- `_detect_subnet_for_bridge()` — Subnet detection from rules
- `get_tap_devices()` — TAP devices on bridge
- `is_bridge_alive()` — Bridge existence check
- Imported by `core/network.py` and `core/network_manager.py`

### id_lookup.py
- `resolve_single_by_id_prefix(prefix, find_fn, cache_dir, label)` — resolves short-ID prefix to exactly one item
- Raises on ambiguous prefix (multiple matches) or not found
- Used by `core/` and `api/` layers for `mvm vm rm <prefix>`, `mvm image rm <prefix>`, etc.

### progress.py
- `ASCIIProgressBar` class — TTY/non-TTY ASCII progress display
- `download_with_progress()` — Download with progress bar

### disk_size.py
- `parse_disk_size()` — Parse size string to bytes ("512M", "1G")
- `format_sectors_human_readable()` — Sectors to human-readable
- `format_bytes_human_readable()` — Bytes to IEC units
- `format_disk_size()` — Bytes to compact format

### resize.py
- `resize_rootfs()` — Resize image to target size using qemu-img

### full_hash.py
- `generate_vm_id()` — 16-char VM ID
- `generate_full_hash_image()` — 64-char image hash
- `generate_full_hash_kernel()` — 64-char kernel hash
- `generate_full_hash_binary()` — 64-char binary hash
- `generate_full_hash_vm()` — 64-char VM hash
- `generate_full_hash_network()` — 64-char network hash
- `shorten_hash()` — Truncate hash for display

### debug_state.py
Global debug mode state management.
- `set_debug_mode()` — Set debug state
- `is_debug_mode()` — Query debug state

### error_handler.py
- `handle_mvm_error()` — Format and print error, exit with code
- Centralized error formatting: maps MVMError subclasses to user-friendly messages
- Used by CLI layer to translate typed exceptions into formatted output

### partition_detection.py
Root partition detection using weighted detector heuristics.
- `PartitionDetector` protocol — Interface for detectors
- `RootPartitionDetector` class — Main detector orchestrator
- `TypeCodeDetector` class — GPT/MBR type code scoring
- `LabelDetector` class — Filesystem label scoring
- `SizeDetector` class — Partition size scoring
- `FilesystemDetector` class — Filesystem type scoring

## ANTI-PATTERNS

| Forbidden | Correct |
|-----------|---------|
| Import `core/` or `api/` | Utils are leaf nodes — no upward deps |
| `print()` in utils | `console.py` helpers only, and only where appropriate |
| Raise domain exceptions | Raise `ValueError` or `ProcessError` — not `VMError` etc. |
| Hardcode paths | Always read from env via `fs.get_cache_dir()` / `fs.get_config_dir()` |
| Scatter tool wrappers in `core/` | Centralize in `utils/` (guestfs, http, process, network) |
