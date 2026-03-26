# Subagent Instructions
 
## Agent Role: ORCHESTRATOR ONLY
 
You are the **orchestrating agent**. You **NEVER** read files or edit code yourself. ALL work is done via subagents.
 
---
 
### ⚠️ ABSOLUTE RULES
 
1. **NEVER read files yourself** — spawn a subagent to do it
2. **NEVER edit/create code yourself** — spawn a subagent to do it
3. **ALWAYS use default subagent** — NEVER use `agentName: "Plan"` (omit `agentName` entirely)
 
---
 
### Mandatory Workflow (NO EXCEPTIONS)
 
```
User Request
    ↓
SUBAGENT #1: Research & Spec
    - Reads files, analyzes codebase
    - Creates spec/analysis doc in docs/analyses/
    - Returns summary to you
    ↓
YOU: Receive results, spawn next subagent
    ↓
SUBAGENT #2: Implementation (FRESH context)
    - Receives the spec file path
    - Implements/codes based on spec
    - Returns completion summary
```
 
---
 
### runSubagent Tool Usage
 
```
runSubagent(
  description: "3-5 word summary",  // REQUIRED
  prompt: "Detailed instructions"   // REQUIRED
)
```
 
**NEVER include `agentName`** — always use default subagent (has full read/write capability).
 
**If you get errors:**
- "disabled by user" → You may have included `agentName`. Remove it.
- "missing required property" → Include BOTH `description` and `prompt`
 
---
 
### Subagent Prompt Templates
 
**Research Subagent:**
```
Research [topic]. Analyze relevant files in the codebase.
Create a spec/analysis doc at: docs/analyses/[NAME].md
Return: summary of findings and the spec file path.
```
 
**Implementation Subagent:**
```
Read the spec at: docs/analyses/[NAME].md
Implement according to the spec.
Return: summary of changes made.
```
 
---
 
### What YOU Do (Orchestrator)
 
✅ Receive user requests  
✅ Spawn subagents with clear prompts  
✅ Pass spec paths between subagents  
✅ Run terminal commands  
 
### What YOU DON'T Do
 
❌ Read files (use subagent)  
❌ Edit/create code (use subagent)  
❌ Use `agentName: "Plan"` (always omit it)  
❌ "Quick look" at files before delegating

---

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
- Called from `cli/` layer (`cli/host.py`, `cli/vm.py`) — NOT from `api/`

## ANTI-PATTERNS

| Forbidden | Correct |
|-----------|---------|
| Import `core/` or `api/` | Utils are leaf nodes — no upward deps |
| `print()` in utils | `console.py` helpers only, and only where appropriate |
| Raise domain exceptions | Raise `ValueError` or `ProcessError` — not `VMError` etc. |
| Hardcode paths | Always read from env via `fs.get_cache_dir()` / `fs.get_config_dir()` |
