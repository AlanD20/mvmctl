# mvmctl/utils/ — Shared Helpers

**Scope:** Pure, domain-agnostic utilities; no business logic, no Firecracker knowledge
**Status:** Pre-production project — refactoring MUST NOT create legacy migration logic.
**Rule:** Never import from `core/`, `api/`, or `cli/`; zero side effects unless explicitly called

## STRUCTURE

```
src/mvmctl/utils/
├── console.py      # Lazy Rich console + print_* helpers
├── fs.py           # Cache/config path resolution; SUDO_USER-aware home
├── http.py         # Resumable download with SHA256 verify
├── process.py      # subprocess wrappers raising ProcessError
├── validation.py   # Entity name, boot arg, and IP validators
└── audit.py        # Append-only audit log → ~/.cache/mvmctl/audit.log
```

**Package `__all__`:** Only `console`, `fs`, `http` are re-exported from `utils/__init__.py`.
`process`, `validation`, and `audit` are used throughout the codebase but not package-exported — import them directly.

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
- Called from `api/` layer for state-changing operations

## ANTI-PATTERNS

| Forbidden | Correct |
|-----------|---------|
| Import `core/` or `api/` | Utils are leaf nodes — no upward deps |
| `print()` in utils | `console.py` helpers only, and only where appropriate |
| Raise domain exceptions | Raise `ValueError` or `ProcessError` — not `VMError` etc. |
| Hardcode paths | Always read from env via `fs.get_cache_dir()` / `fs.get_config_dir()` |
