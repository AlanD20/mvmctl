# mvmctl

MicroVM Manager — a speed-first CLI for managing Firecracker microVMs. Provides fast VM lifecycle management, networking, image provisioning, and console/SSH access.

## Language

### Domain
A business capability with isolated logic. Each domain (vm, network, image, kernel, binary, key, host, config, cache, volume, console, logs, cloudinit, ssh) lives in `core/{domain}/` and consists of Controller, Service, Repository, and Resolver files. Domains do NOT import other domains.
_Avoid_: Module, component, service (overloaded terms)

### Intra-domain orchestration
Work that sequences multiple operations within a single domain (e.g., teardown NAT → remove bridge → delete DB record). Lives in core/ Service classes.
_Avoid_: Orchestration in Controller

### Cross-domain orchestration
Work that coordinates across multiple domains (e.g., VM creation orchestrates vm + network + image + kernel + cloudinit). Lives exclusively in api/ *Operation classes.
_Avoid_: Cross-domain imports in core/

### Controller (stateful)
A class bound to a single entity instance. Handles lifecycle state transitions (start, stop, pause, resume, snapshot). Does NOT validate input. Does NOT orchestrate. Does NOT handle CRUD creation/removal.
_Avoid_: Controller.remove(), Controller.create()

### Service (stateless)
A class for stateless intra-domain operations. Handles infrastructure operations (bridges, TAPs, NAT, subprocesses). Performs state detection (checking current system state as part of an operation). Guards invariants that protect against system damage. Does NOT validate caller input.
_Avoid_: Validation gatekeeping in Service

### Repository
A class for database CRUD operations. ALL SQL queries live here — single file, no separate Inventory/Query classes. Uses SQL-level computation (COUNT, WHERE IN), never fetch-all-then-filter.
_Avoid_: Business logic in Repository

### Resolver
A class for entity resolution by identifier (name, ID, IP, MAC to domain object). Uses `RELATIONS` dict + `RelationEnricher` for batch relation loading.
_Avoid_: DB queries or business logic in Resolver

### Validation (caller's responsibility)
Checks that input is structurally valid: format, existence, cross-field constraints. Belongs in API layer (*Input or *Request classes). Does NOT belong in Service or Controller. The caller (API layer) is responsible for passing clean, validated data down.
_Avoid_: Defensive validation in Service/Controller

### State detection (operation's responsibility)
Checks that are inherently part of executing an operation — detecting whether the system is in state A or B to decide the execution path (e.g., "does this bridge exist?" to branch between create vs reconcile). Belongs in Service/Controller as part of the operation's logic.

### Invariant guard
A check in Service that protects against system damage (e.g., "are TAPs still attached?" before removing NAT rules). The one exception to "Service does not validate" — these guard against partial-failure states, not invalid input.

### Speed-first principle
Every architectural decision is weighed against its runtime cost. Avoid redundant subprocess calls, unnecessary dataclass allocations, and deep call chains. A 10ms subprocess check that duplicates what the operation already detects is a bug.
_Avoid_: "Clean architecture" for its own sake

### Operation class
A collection of static methods in `api/{domain}_operations.py`. The ONLY place where multiple domains are imported and sequenced. Handles cross-domain orchestration, cross-domain data passing (e.g., querying VMRepository for a network's reference check, then passing results to NetworkService).
_Avoid_: Core domain files executing cross-domain logic

### Public API boundary
The `mvmctl.api` package IS the stable, curated public interface for all consumers — CLI, future TUI/GUI, and external scripts. It lazily re-exports all Operation classes and Input types via `__init__.py`. External code should `from mvmctl.api import VMOperation, VMCreateInput` and nothing else. The `mvmctl.core` package is an implementation detail. The CLI layer is just one frontend — it has no special access privileges.
_Avoid_: External consumers importing from `mvmctl.core` or `mvmctl.cli`

## Relationships

- An **Operation** class orchestrates across **Domains**
- A **Service** performs **intra-domain orchestration** using **Controller** and **Repository**
- A **Controller** manages state transitions for exactly one entity
- A **Repository** provides data access for its **Domain** only
- **Validation** runs in the API layer before data reaches **Service** or **Controller**
- **State detection** runs inside **Service** or **Controller** as part of execution
- **Invariant guards** may appear in **Service** when the guard prevents system damage

### Exception hierarchy
A 3-level hierarchy: `MVMError` (root) → `{Domain}Error` (domain category) → `{Domain}{Specific}Error` (specific issue). Every exception carries an optional `code` string for fine-grained programmatic branching in the API layer. The `code` enables auto-detection/auto-handling without parsing message text.

The `MVMError` base class has an optional `code: str | None` parameter:
```python
class MVMError(Exception):
    def __init__(self, message: str = "", *, code: str | None = None) -> None:
        self.code = code
        super().__init__(message)
```

### API result types
The API layer returns three types that the CLI/TUI/GUI consumes:
- **`OperationResult[T]`** — Single operation result with `status` (success/error/warning), `code` (machine-readable), `message` (user-facing), `item` (payload), and optional `exception`.
- **`BatchResult[T]`** — Collection of `OperationResult` items from bulk operations.
- **`NeedsInteraction`** — Returned when the operation requires user action (e.g., sudo password prompt). The frontend checks for this type before treating the result as complete.

```
MVMError                                   # Root — carries optional code field
├── NetworkError                            # Domain category
│   └── NetworkSubnetOverlapError           # Specific issue with code="SUBNET_OVERLAP"
├── VMError                                 # Missing — needs to be created
│   ├── VMCreateError                       # Currently MVMError directly
│   └── VMStateError                        # Currently MVMError directly
├── FirecrackerError                        # Missing — needs to be created
│   ├── FirecrackerClientError             # Currently MVMError directly
│   ├── FirecrackerSpawnError              # Currently MVMError directly
│   └── FirecrackerConfigError             # Currently MVMError directly
└── ... (existing: ImageError, KernelError, BinaryError, etc.)
```

Error message format (user-facing, three parts in one line):
```
What happened. Why it happened. Possible fix.
```

Error codes format: Dot-separated with domain prefix. Hierarchical and self-documenting.
```
network.subnet.overlap        # NetworkError → NetworkSubnetOverlapError
vm.create.binary_not_found    # VMError → VMCreateError (hyphen for multi-word)
host.init.sudoers_failed      # HostError → HostInitError
```

### Error handling pattern
- **Service/Controller**: Raise typed exceptions with `logger.error()` before each raise. Use `code` parameter for programmatic distinction.
- **API layer**: Catch typed exceptions, branch on `isinstance()` or `e.code` for auto-handling, convert to `OperationResult`/`BatchResult` for CLI display.
- **Repository**: Let DB exceptions propagate. `_graceful_read` decorator handles DB availability at the boundary.
- No bare `except:`, no `except Exception` that swallows typed errors.

### Logging pattern
- **Log before raise**: Every `raise` in Service/Controller has a preceding `logger.error()` or `logger.warning()` with operational context (parameters, state, failure reason).
- **Log message**: Operator-facing — includes module context, parameter values, and the root cause.
- **Exception message**: User-facing — "what happened. why. possible fix." short summary.
- **API layer**: `logger.info()` for success, `logger.warning()` for recoverable issues.

### Coding style
- **Method length**: No hard limit. 50+ lines is fine if logic is linear and clear.
- **Private helpers**: Only for reused logic or genuinely complex operations (not for single-use trivial extraction).
- **Early returns**: Prefer early returns over nested if/else branching.
- **Explicit typing**: All function signatures must have explicit types. No `Any`, no implicit `Optional`. Use `from __future__ import annotations`.
- **`from __future__ import annotations`**: Required as the first import (after file docstring) in EVERY `.py` file under `src/mvmctl/`. Enables PEP 563 postponed evaluation so forward references work without string quotes.
- **No quoted annotations**: With `from __future__ import annotations`, write types directly: `def get(x: str) -> VMInstanceItem | None` — no quotes around annotations.
- **Centralized infrastructure**: Subprocess calls, file I/O, and system interactions must go through shared utility functions (e.g., `NetworkUtils._run_batch()`, `privileged_cmd()`). Every tool invocation must have one canonical path.
- **Docstrings**: Public classes get 1-3 lines. Public methods get docstrings only when behavior is non-obvious. Private methods get no docstrings — name explains it. Inline comments only for WHY, never for WHAT. The code should be self-explanatory; comments justify counter-intuitive choices.

### Lazy imports
ALL `__init__.py` files use PEP 562 `__getattr__` + `resolve_lazy()` from `mvmctl.utils._lazy_import`. Eager imports at package level are forbidden. When any module does `from mvmctl.core.vm import VMController`, only `_controller.py` is loaded — not the entire domain.

Pattern:
```python
from __future__ import annotations
from mvmctl.utils._lazy_import import resolve_lazy

__all__ = ["ExportedClass1", ...]

_LAZY_MAP: dict[str, tuple[str, str]] = {
    "ExportedClass1": ("path._submodule", "ExportedClass1"),
}

def __getattr__(name: str) -> object:
    return resolve_lazy(name, _LAZY_MAP, __name__)

def __dir__() -> list[str]:
    return __all__
```

### Subprocess invocation
ONE canonical path for all subprocess calls: `run_cmd()` / `stream_cmd()` in `mvmctl.utils._system`. No raw `subprocess.run()` except in these utility functions.
```python
# ✅ Correct — everything routes through the centralized runner
from mvmctl.utils._system import run_cmd
result = run_cmd(["ip", "link", "set", tap, "down"], privileged=True)

# ❌ Forbidden — raw subprocess.run scattered across modules
subprocess.run(["iptables", ...], check=True)
```

The centralized runner provides: consistent logging (`logger.debug` of the command), privilege escalation via `privileged_cmd()`, timeout enforcement, and uniform error formatting.

### Import conventions
| Layer | Imports from | Example |
|-------|-------------|---------|
| **CLI** | `mvmctl.api` (public surface) | `from mvmctl.api import VMOperation, VMCreateInput` |
| **API** | `mvmctl.api.inputs` (public input surface) | `from mvmctl.api.inputs import VMCreateInput` |
| **API** | `mvmctl.core.{domain}` (public domain surface) | `from mvmctl.core.vm import VMController` |
| **API** | `mvmctl.core._shared` (public infrastructure) | `from mvmctl.core._shared import Database` |
| **API** | `mvmctl.utils.*` (shared helpers) | `from mvmctl.utils._system import privileged_cmd` |
| **Core** | `mvmctl.core._shared` only | `from mvmctl.core._shared._db import Database` |
| **Core** | Own sibling modules | `from mvmctl.core.vm._firecracker import FirecrackerClient` |
| **Utils** | Nothing from core/api/cli | N/A — leaf nodes |

**Forbidden:** Importing `mvmctl.core.{domain}._{private_module}` from API/CLI. Always use the `__init__.py` re-export.

## Flagged ambiguities

- "Orchestration" was used to mean both intra-domain and cross-domain sequencing — resolved: intra-domain orchestration lives in Service (core/), cross-domain orchestration lives in Operation (api/). These are different concepts.
- "Validation" was overloaded to include format checks, existence checks, system state checks, and invariant guards — resolved: format and existence checks are **validation** (caller's job), system state detection is part of the operation (Service's job), and invariant protection against damage is a narrow exception.
- "Validation in Service" was proposed as a norm — resolved: rejected. Caller validates, receiver trusts. The Service's role is execution, not gatekeeping.
- "Controller" was being used for CRUD operations (remove, create) — resolved: Controller is state management only. CRUD orchestration belongs in Service or Operation.
- "Multiple VMM backends" was considered (Firecracker + Cloud Hypervisor + QEMU) — resolved: Firecracker-only for v0.1. The entire VM lifecycle is Firecracker-shaped (HTTP API over UDS, JSON config schema, vsock console relay). Adding backends would require 3-5x effort per supported VMM and is deferred until a concrete second VMM is needed with a committed timeline. The existing `api/` layer boundary is the right future seam for VMM abstraction.
- "System test assertion depth" was ambiguous (returncode? stdout? JSON?) — resolved: **Option C** verification. Every system test must verify actual system state at the deepest practical level: JSON field assertions, filesystem checks (`os.path.exists`, `Path.readlink`, `Path.stat`), process checks (`/proc/$PID`), iptables checks (`sudo iptables -L`), and/or direct SQLite queries on `~/.cache/mvmctl/mvmdb.db`. Returncode-only assertions are explicitly forbidden in system tests. A test that does not verify business outcomes is incomplete.
- "Which tests can be shallow" was ambiguous — resolved: **none**. All returncode-only tests must be upgraded to Option C before release. If a test only checks `result.returncode == 0` without verifying system state, it is incomplete and must be fixed.
- "What level of CLI coverage is required" was ambiguous — resolved: **gap matrix must be zero**. Every CLI subcommand and every flag on every command must have a system test covering both happy path and error edge cases. Any untested command or flag is a blocking release risk.
- "Who owns tests/" was ambiguous — resolved: **QA engineer agent** is the sole owner and operator of `tests/`. The engineer agent is strictly forbidden from touching any file under `tests/` at any cost (read, write, edit, create, delete, rename, patch). The only legitimate interaction with tests/ from the engineer agent is running `uv run pytest` when explicitly asked.
- "Where does system test structure live" — resolved: **AGENTS.md files under tests/ are removed**. All system test structure, file listing, marker registry, and design rules now live in `.opencode/agent/qa-engineer.md` and `CONTEXT.md`. No scattered AGENTS.md files.
- "What about expensive/infrequent tests" — resolved: two **exclusive markers** added. `pytest.mark.kernel_build` for kernel build-from-source tests (10+ min, needs gcc/make). `pytest.mark.host_reset` for host clean/reset with sudo (modifies real system state). Both excluded from default `pytest tests/system/` runs. Invoke explicitly with `-m kernel_build` or `-m host_reset`.
- "What about parallel test safety" — resolved: every test that modifies shared state (defaults, cache, assets, binaries, kernels) must be marked `pytest.mark.serial` to prevent xdist race conditions.
- "How do system tests handle sudo operations" — resolved: `sudo` is allowed for `mvm init`, `mvm host init`, `mvm host clean`, and `mvm host reset`. Use the built binary at `~/.local/bin/mvm` for sudo operations. The QA engineer agent has explicit sudo permission for these four command patterns.
- "What is the release gate" — resolved: the release gate is **system tests passing against `dist/mvm`**. Before reporting release ready, the QA engineer must: (1) build `dist/mvm` via `scripts/build_services.py --fast`, (2) copy to `~/.local/bin/mvm`, (3) run all system tests against the binary, (4) report pass/fail status.

## System Tests

### System test
A black-box CLI integration test under `tests/system/` that invokes `mvm` via real
subprocess (no mocking, no `CliRunner`). Operates against the real system — real
kernel, real images, real binaries, real bridges, real SQLite DB at
`~/.cache/mvmctl/mvmdb.db`. Every system test must verify actual business outcomes
at the OS level: JSON state, filesystem state, process state, iptables state, and/or
DB state. System tests are the primary release gate.
_Avoid_: Shallow returncode-only test that proves nothing about system state.

### Option C verification
The thoroughness standard for system test assertions. Every system test verifies
system state at the deepest practical level: JSON field assertions from `* ls --json`,
file existence/symlink checks via `os.path`, process presence via `/proc/$PID`,
iptables rule presence via `sudo iptables -L`, and/or direct SQLite queries on
`~/.cache/mvmctl/mvmdb.db`. A test that only checks `returncode == 0` is incomplete.
Named "Option C" to distinguish from minimal returncode-only (A) and moderate JSON-only (B).

### Gap matrix
A cross-reference of every CLI subcommand and flag against its system test coverage.
Built by reading every file in `src/mvmctl/cli/` and every file in `tests/system/`.
All gaps must be filled — any untested command or flag is a blocking release risk.

### Edge case categories (8 categories)
For every CLI flag, check all eight: happy path (with state verify), missing required
args, invalid values, boundary values, JSON output format, confirmation prompts,
non-existent resources, duplicate creation.

### Marker
A `pytest.mark.*` annotation on a test class or function. System test markers include:
`system` (always), `domain_<name>` (file-level filter), `slow` (>30s), `serial`
(modifies shared state — prevent xdist races), `requires_kvm` (needs /dev/kvm),
`requires_network` (needs real bridges), `kernel_build` (build from source, excluded
from default run), `host_reset` (host clean/reset with sudo, excluded from default run).

### Serial test
A test marked `pytest.mark.serial` because it modifies shared system state (default
image, default network, cached binaries, kernel defaults). Serial tests must not run
in parallel with each other to prevent race conditions on shared resources. Every test
that changes defaults, removes assets, or modifies global state must be serial.

### Non-destructive test
A test that does not modify persistent state — it reads JSON, inspects resources,
lists records. Non-destructive tests run FIRST in every file, before any destructive
test that might remove or alter the resources they depend on.

### Destructive test
A test that modifies persistent state — removes a resource, changes a default, prunes
cache, cleans host. Destructive tests MUST be defined at the END of their file, after
all non-destructive tests. Every destructive test must restore any removed state
(re-pull image, recreate network) in a `finally` block.

### Kernel build marker (`pytest.mark.kernel_build`)
A pytest marker that designates tests requiring kernel compilation from source
(`kernel pull --type official` with build flags like `--jobs`, `--keep-build-dir`,
`--clean-build`, `--config`). These tests require a full build toolchain (gcc, make,
bc, bison, flex) and take 10+ minutes. EXCLUDED from default system test runs.
Invoke explicitly: `pytest -m kernel_build`.

### Host reset marker (`pytest.mark.host_reset`)
A pytest marker that designates tests executing `host clean` or `host reset` with
sudo, which modifies real system state (bridges, iptables, sysctl, group memberships).
EXCLUDED from default system test runs. Invoke explicitly: `pytest -m host_reset`.

### Tautological test
A test that verifies something trivially true by construction, proving nothing about
the system. Examples: asserting CREATE output contains the name you just passed,
checking `--help` contains "Usage:", asserting `returncode == 0` without verifying
the downstream effect. Forbidden in system tests.
