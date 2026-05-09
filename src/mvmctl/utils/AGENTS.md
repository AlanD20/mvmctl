# mvmctl/utils/ — Shared Helpers

**Scope:** Pure, domain-agnostic utilities; no business logic, no Firecracker knowledge
**Status:** Pre-production project — refactoring MUST NOT create legacy migration logic.
**Rule:** Never import from `core/`, `api/`, or `cli/`; zero side effects unless explicitly called

## STRUCTURE

```
src/mvmctl/utils/
├── __init__.py          # Package entry — re-exports _disk, _io, _system, _validators, crypto, fs, http
├── _disk.py             # Disk size parsing/formatting + root partition detection (PartitionDetector etc.)
├── _io.py               # Console output helpers — print_info, print_table, setup_logging, log_exception
├── _lazy_import.py      # Reusable lazy import helper for module __init__.py re-exports
├── _system.py           # Subprocess wrappers — run_cmd, stream_cmd, privileged_cmd; signal handling; ProcessSignalHandler
├── _validators.py       # Combined validation — KeyValidator, NetworkValidator, VMValidator classes
├── auditlog.py          # Append-only audit log — AuditLog class (structured JSON lines)
├── cli.py               # CLI utilities — CliUtils.check_name_arg, handle_errors decorator
├── common.py            # Common utilities — CacheUtils (path resolution), CommonUtils (validation/formatting), debug state
├── crypto.py            # SHA256 hash generation — HashGenerator (image, kernel, binary, vm, network hashes)
├── fs.py                # Filesystem operations — FsUtils (read_json/yaml/raw, secure_mkdir, chown, pid files)
├── http.py              # HTTP downloads — HttpDownload (resumable, SHA256 verify, retry, cache), HttpCache
├── network.py           # Network utilities — NetworkUtils (MAC/TAP/bridge, iptables, subnet math, interface queries)
├── operation_utils.py   # Operation utilities — bridges between raw progress and UI events
├── progress.py          # ASCII progress bars — ASCIIProgressBar, Spinner (threaded indeterminate spinner)
├── template.py          # String template rendering — render_template, render_optional_template
└── yaml.py              # YAML field extraction — require_str, optional_str, optional_int, require_str_list, parse_set_val_list
```

**Package `__all__`:** Only `_disk`, `_io`, `_system`, `_validators`, `crypto`, `fs`, `http` are re-exported from `utils/__init__.py`. Other modules are used throughout the codebase but not package-exported — import them directly.

## MODULE DETAILS

### _disk.py
Disk size parsing/formatting and root partition detection (merged into one module).
- **Size parsing:** `parse_disk_size("512M")` → bytes; `format_sectors_human_readable()` → "1.5 GiB"; `format_disk_size()` → "1.5G"
- **Partition detection:** `PartitionDetector` protocol; `RootPartitionDetector` orchestrator with `TypeCodeDetector`, `LabelDetector`, `SizeDetector`, `FilesystemDetector` — weighted heuristic scoring to identify the root filesystem partition
- Raises `MVMError` for invalid size formats, `RootPartitionDetectionError` / `TieDetectedError` for detection failures

### _io.py
Console output helpers and logging setup — a Rich shim for non-TTY environments.
- `_PlainConsole` class — drop-in Rich Console shim (discards Rich-specific kwargs)
- `print_info()`, `print_warning()`, `print_error()`, `print_success()` — Plain-text formatted output
- `print_table(columns, rows)` — Column-aligned plain-text tables
- `print_section_header()`, `print_inspect_header()` — Section/inspect headers
- `print_key_value()` — Key-value pair formatting
- `get_state_marker()`, `get_combined_marker()` — Status markers for resource listings
- `setup_logging(verbose, debug)` — Configure root logger respecting `MVM_LOG_LEVEL` env var
- `get_logger(name)` — Logger factory; `log_exception(logger, msg, exc)` — Exception logging with traceback when DEBUG is on
- **Only** CLI layer should call print helpers; `core/` must raise exceptions instead

### _system.py
Subprocess wrappers, signal handling, and process lifecycle management.
- `run_cmd(args, ...)` → `CompletedProcess` — list form only; raises `ProcessError` on failure
- `stream_cmd(args, ...)` → yields stdout lines — for long-running builds
- `_is_sudo_cached()`, `_validate_sudo_credentials()` — Sudo credential cache with 60s TTL
- `privileged_cmd(cmd)` — Prepends sudo if not root; validates mvm group membership
- `require_mvm_group_membership()` — Checks /etc/group and process group membership
- `is_process_running(pid)` — os.kill(pid, 0) check
- `has_python_ancestor(pid)` — Walks /proc PPID chain looking for python/mvm processes
- `SigtermContext` / `sigterm_context()` — SIGTERM handler context manager with cleanup
- `ProcessSignalHandler` — Full lifecycle manager: zombie detection, graceful shutdown (SIGTERM → wait → SIGKILL), PID reuse mitigation, D-state awareness

### _validators.py
Domain-specific validation classes for keys, networks, and VMs.
- `KeyValidator.validate_name(name)` — Delegates to CommonUtils.validate_entity_name
- `NetworkValidator` — `validate_name()` (alphanumeric + hyphen/underscore, 1-31 chars, no dots, no reserved), `validate_subnet()` (CIDR format), `validate_ipv4_gateway()`, `validate_ipv4_address()` (with subnet/gateway constraints), `validate_bridge_name()` (IFNAMSIZ, dangerous chars, existence check), `validate_nat_gateways()`, `is_ip_address()`, `validate_mac()`, `validate_subnet_no_overlap()`
- `VMValidator` — `validate_name()`, `validate_boot_arg_component()` (injection chars), `validate_ssh_username()` (POSIX pattern), `validate_boot_args()` (full boot arg validation with UUID format check)
- All raise `MVMError` on validation failure

### auditlog.py
Append-only structured audit log via logging.
- `AuditLog` class — Singleton logger writing to `~/.cache/mvmctl/audit.log`
- `AuditLog.log(operation, changes, context)` — Writes structured entry: `[timestamp UTC] user=X op=Y changes=k1=v1,... context="..."`
- Calls from `cli/` layer only — NOT from `api/` or `core/`

### cli.py
Domain-agnostic CLI helpers for Typer commands.
- `CliUtils.check_name_arg(ctx, name)` — Guard for positional name args (shows help on "help" or None)
- `handle_errors` decorator — Wraps Typer commands; catches MVMError, PrivilegeError, KeyboardInterrupt, BrokenPipeError, and unexpected exceptions; prints clean user-friendly error to stderr via Rich; exits with code 1
- `_print_error(message)` — Colored single-line error to stderr

### common.py
Common utilities shared across all layers.
- **Debug state:** `set_debug_mode(bool)`, `is_debug_mode()` — Global debug flag
- `CacheUtils` — `get_cache_dir()`, `get_config_dir()`, `get_config_path()`, `get_mvm_db_path()`, `get_temp_dir()`, `get_vms_dir()`, `get_vm_dir(id)`, `get_images_dir()`, `get_kernels_dir()`, `get_bin_dir()`, `get_logs_dir()`, `get_keys_dir()`, `get_warm_image_dir()` — All env-var aware with SUDO_USER home resolution; `resolve_dir()` ensures directory exists (with `CONST_DIR_PERMS_CACHE` mode)
- `CommonUtils` — `validate_entity_name()` (defense-in-depth: dangerous chars, reserved names, IP-like, pattern), `contains_dangerous_chars()`, `is_reserved_name()`, `sanitize_for_log()`, `human_readable_datetime()` (ISO→"YYYY/MM/DD HH:MM:SS"), `format_bytes_human_readable()` (IEC binary units), `coerce()` (type coercion), `safe_int()` (safe extraction)

### crypto.py
SHA256 hash generation for content-addressed domain resources.
- `HashGenerator` — All methods return 64-char lowercase SHA256 hexdigests
- `HashGenerator.image(os_slug, source, timestamp)` — Image identity hash
- `HashGenerator.kernel(file_path, version, arch, timestamp)` — Kernel hash (incorporates file content hash)
- `HashGenerator.binary(file_path, name, version)` — Binary hash (incorporates file content hash)
- `HashGenerator.vm(name, created_at)` — VM hash truncated to 32 chars (for Unix socket path length limits)
- `HashGenerator.network(name, subnet, created_at)` — Network hash
- `HashGenerator.shorten(full_hash, length=12)` — Truncate hash for display

### fs.py
Filesystem utilities with symlink-attack resistant operations.
- `FsUtils._open_nofollow(path)` — Open with O_RDONLY + O_CLOEXEC + O_NOFOLLOW
- `FsUtils.read_json(path)` / `read_yaml(path)` / `read_raw(path)` — Read files with O_NOFOLLOW protection
- `FsUtils.secure_mkdir(directory, name)` — Create directory, refusing symlinks (race-condition aware)
- `FsUtils.write_pid_file(pid_file, pid)` — Write PID with flock locking
- `FsUtils.get_real_user_ids()` — (uid, gid) of invoking user under sudo
- `FsUtils.chown_to_real_user(path)` — Recursive chown to invoking user

### http.py
HTTP download utilities with SHA256 verification and response caching.
- `HttpCache` — File-based response cache with TTL, atomic write via temp file + os.replace
- `@_with_retry()` — Retry decorator with exponential backoff (configurable max_retries, delay, backoff)
- `HttpDownload._download(url, ...)` — Low-level fetch returning bytes; optional cache
- `HttpDownload.head_size(url, ...)` — Remote file size via HEAD with caching
- `HttpDownload.read_raw_content(url, ...)` — Fetch text content (e.g., SHA256 sidecar files)
- `HttpDownload.read_json_content(url, ...)` — Fetch and parse JSON
- `HttpDownload.with_download(url, dest, ...)` — Pure transport: HTTP + retries + atomic os.replace; optional progress_callback and on_start
- `HttpDownload.download_file(url, dest, expected_sha256, ...)` — Full orchestration: transport + SHA256 verify + ASCII progress bar + missing-checksum confirmation via Typer
- Sets `User-Agent: mvmctl/{version}`
- Raises `HttpDownloadError`, `ChecksumMismatchError`

### network.py
Network computation and system query utilities — all static methods on `NetworkUtils`.
- **Subnet math:** `compute_subnet_mask()`, `compute_prefix_length()`, `compute_ipv4_gateway()` (RFC 3021 aware), `compute_bridge_address()`, `compute_bridge_name()`
- **Naming & generation:** `generate_mac(prefix)` (secrets-based), `generate_tap_name(network, vm)` (random suffix)
- **IP allocation:** `allocate_next_ip(existing_ips, subnet, gateway)` — First-fit from subnet hosts
- **System queries:** `get_physical_interfaces()` (/sys/class/net, filtered), `detect_outbound_interface()` (ip route show default), `bridge_exists()`, `tap_exists()`, `chain_exists()` (iptables), `get_tuntap_devices()`, `get_bridges()`, `get_bridge_taps()`, `get_tap_bridge()`, `ensure_interface_ready()` (UP + IPv4 check)
- **iptables:** `detect_iptables_backend_conflict()` (nft vs legacy), `strip_tap_rules()` (filter TAP rules from iptables-save output)
- **Internal:** `_run_batch(commands)` (ip -batch mode), `bridge_has_subnet()`
- Raises `NetworkError`

### progress.py
ASCII text-based progress indicators for downloads and indeterminate operations.
- `ASCIIProgressBar(total, width, title)` — Shows `[####      ] 45% (4.2MB/10MB)`; TTY: in-place updates via `\r\033[K`; non-TTY: newline per update; `.update(n)` / `.finish()`
- `Spinner(message)` — Threaded indeterminate spinner with Unicode braille frames; `.start()` / `.stop(done_message)`; context manager support

### template.py
Minimal string template rendering with variable substitution.
- `render_template(template, variables)` — `template.format(**variables)`; raises `ValueError` on missing keys
- `render_optional_template(template, variables)` — Null-safe version; returns None on None input

### yaml.py
Typed field extraction helpers for YAML-parsed dictionaries (from `yaml.safe_load`).
- `require_str(data, key)` → `str` — Required string field; raises `ValueError` if absent/not-a-string
- `optional_str(data, key)` → `str | None` — Optional string field
- `optional_int(data, key)` → `int | None` — Optional integer field
- `require_str_list(data, key)` → `list[str]` — Required list-of-strings (absent → empty list)
- `parse_set_val_list(data, key)` → `list[tuple[str, str]]` — Option/value pairs from `{option, value}` mappings or 2-element sequences

## ANTI-PATTERNS

| Forbidden | Correct |
|-----------|---------|
| Import `core/` or `api/` | Utils are leaf nodes — no upward deps |
| `print()` in utils | `_io.py` helpers only, and only where appropriate |
| Raise domain exceptions | Raise `ValueError`, `ProcessError`, `NetworkError` — not `VMError` etc. |
| Hardcode paths | Always read from env via `CacheUtils.get_cache_dir()` / `get_config_dir()` |
| Scatter tool wrappers in `core/` | Centralize in `utils/` (_system, http, network, fs) |
