# mvmctl/utils/ — Shared Helpers

**Scope:** Pure, domain-agnostic utilities; no business logic, no Firecracker knowledge
**Status:** Pre-production project — refactoring MUST NOT create legacy migration logic.
**Rule:** Never import from `core/`, `api/`, or `cli/`; zero side effects unless explicitly called

## STRUCTURE

```
src/mvmctl/utils/
├── console.py      # Lazy Rich console + print_* helpers
├── fs.py           # Cache/config path resolution; SUDO_USER-aware home; test path helpers
├── http.py         # Resumable download with SHA256 verify
├── process.py      # subprocess wrappers raising ProcessError
├── validation.py   # Entity name, boot arg, and IP validators
├── audit.py        # Append-only audit log → ~/.cache/mvmctl/audit.log
├── guestfs.py      # ALL libguestfs operations (OptimizedGuestfs, check_libguestfs, extract_partition_with_guestfs)
├── template.py     # Template rendering for Firecracker boot config
├── time.py         # Human-readable time formatting ("5 minutes ago")
├── yaml.py         # YAML loading helpers
├── network.py      # Network utilities: MAC generation, TAP naming, IP allocation, subnet helpers
├── id_lookup.py    # Short-ID prefix matching utilities
├── progress.py     # Download progress bars
├── disk_size.py    # Disk size parsing/formatting
├── resize.py       # Image resize utilities
├── full_hash.py    # Full SHA256 hash generation
├── debug_state.py  # Debug state dumping for diagnostics
└── error_handler.py # Centralized error formatting and handling helpers
```

**Package `__all__`:** Only `console`, `fs`, `http` are re-exported from `utils/__init__.py`.
Other modules are used throughout the codebase but not package-exported — import them directly.

## MODULE DETAILS

### console.py
- Lazy `console` (Rich `Console`) — only instantiated on first use
- `print_info(msg)`, `print_warning(msg)`, `print_error(msg)`, `print_success(msg)`
- `print_table(headers, rows)` — Rich table rendering
- **Only** CLI layer and `cli/`-adjacent code should call these; `core/` must raise exceptions instead

### fs.py
- `get_cache_dir()` → `Path` — respects `MVM_CACHE_DIR` env, falls back to `~/.cache/mvmctl/`
- `get_config_dir()` → `Path` — respects `MVM_CONFIG_DIR`, falls back to `~/.config/mvmctl/`
- `get_*_dir(cache_dir)` — per-entity dirs: `vms/`, `images/`, `kernels/`, `networks/`, `keys/`, `bin/`, `logs/`, `assets/`
- `SUDO_USER` home resolution — when run via `sudo`, resolves to the invoking user's home (not root's)

### http.py
- `download_file(url, dest, sha256, progress)` — resumable download; raises on checksum mismatch
- Sets `User-Agent: mvmctl/{version}`
- Used by `image.py`, `kernel.py`, `binary_manager.py` for all asset downloads

### process.py
- `run_cmd(cmd, ...)` → `CompletedProcess` — list form only; raises `ProcessError` on failure
- `stream_cmd(cmd, ...)` → yields stdout lines — for long-running builds (kernel make)
- Always captures stderr and includes in `ProcessError.stderr`

### validation.py
- `validate_entity_name(name)` — VM/network/key names: alphanumeric + hyphen, 1–63 chars
- `validate_boot_arg_component(value)` — safe kernel cmdline component
- `is_ip_address(value)` → `bool`

### audit.py
- `log_audit(action, details)` — appends JSON line to `$MVM_CACHE_DIR/audit.log`
- Called from `cli/` layer (`cli/host.py`, `cli/vm.py`) — NOT from `api/`

### guestfs.py
- `OptimizedGuestfs` — libguestfs wrapper with connection pooling
- `check_libguestfs()` — availability check
- `extract_partition_with_guestfs(...)` — partition extraction
- **ALL** libguestfs operations belong here — never in `core/`

### network.py
- MAC address generation (random, vendor-prefixed)
- TAP device name construction: `mvm-{net[:3]}-{vm[:3]}-{rand3}`
- IP allocation helpers: next available IP in subnet, lease management
- Subnet/CIDR parsing and validation helpers
- Imported by `core/network.py` and `core/network_manager.py`

### id_lookup.py
- `resolve_single_by_id_prefix(items, prefix)` — resolves short-ID prefix to exactly one item
- Raises on ambiguous prefix (multiple matches) or not found
- Used by `core/` and `api/` layers for `mvm vm rm <prefix>`, `mvm image rm <prefix>`, etc.

### debug_state.py
- Dumps complete system state (VMs, networks, assets) for diagnostics/bug reports
- Called by `mvm host` debug commands

### error_handler.py
- Centralized error formatting: maps MVMError subclasses to user-friendly messages
- Used by CLI layer to translate typed exceptions into Rich-formatted output

## ANTI-PATTERNS

| Forbidden | Correct |
|-----------|---------|
| Import `core/` or `api/` | Utils are leaf nodes — no upward deps |
| `print()` in utils | `console.py` helpers only, and only where appropriate |
| Raise domain exceptions | Raise `ValueError` or `ProcessError` — not `VMError` etc. |
| Hardcode paths | Always read from env via `fs.get_cache_dir()` / `fs.get_config_dir()` |
| Scatter tool wrappers in `core/` | Centralize in `utils/` (guestfs, http, process, network) |
