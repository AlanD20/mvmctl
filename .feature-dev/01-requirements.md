# Requirements: firecracker-manager CLI Phase 4

## Problem Statement

Phase 4 is a refinement and polish release of the `firecracker-manager` Python CLI tool for
managing Firecracker microVMs on Linux. The tool's users are developers and infrastructure
engineers who run Firecracker VMs on Linux and need a reliable, well-documented CLI. Phase 4
addresses ambiguities surfaced from real usage in key management, networking, VM lifecycle,
help-command consistency, API documentation, and distribution — making the tool
production-ready for public release.

## Acceptance Criteria

- [ ] All 8 requirement sections in `python-cli-phase-4.md` are implemented
- [ ] Full test suite passes with no failures (`uv run pytest tests/ -v`)
- [ ] `firecracker-manager/docs/API.md` exists and is complete per spec
- [ ] `pyproject.toml` is production-complete (hatchling build, all deps pinned, pipx/uvx-compatible)
- [ ] GitHub Actions `release.yml` builds PyInstaller binaries for ubuntu-22.04 and ubuntu-24.04
- [ ] `phase-4-status.md` created and up-to-date
- [ ] `phase-status.md` updated to show Phase 4 complete

## Scope

### In Scope

**Section 1 — `key` SSH key management (revised)**
- `key ls`: table with Name, Fingerprint, Algorithm, Comment, Date Added
- `key add <name> <path>`: import .pub file, --overwrite flag, print fingerprint on success
- `key create <name>`: ED25519 keypair, --output flag, auto-register pub key, print paths
- `key remove <name>` / `key rm <name>`: remove from cache, warn if used by VM
- `--ssh-key` for `vm create`: name-first resolution, fallback to file path, clear error

**Section 2 — `network` management (revised and clarified)**
- `network ls`: Name, Bridge Device, CIDR, Gateway, VM Count, NAT Enabled; mark default
- `network create <name>`: --cidr (required), --gateway (default .1), --no-nat flag; CIDR overlap check
- `network remove <name>` / `network rm`: fail if VMs attached; default network removable
- `network inspect <name>`: full detail including iptables rules, attached VMs with IPs
- Default network `default` created automatically during `configure`/`host init`
- Lease table at `networks/<name>/leases.json`; auto IP allocation; static --ip validation

**Section 3 — `vm` revised behaviours**
- `vm remove` graceful shutdown: SendCtrlAltDel → wait 5s → SIGTERM → wait 1s → SIGKILL
- Remove PID file, socket, TAP device, release IP lease, `ssh-keygen -R <ip>`, delete VM dir
- `vm pause` / `vm resume`: respond with "not supported" (not unrecognised command error)
- No `vm setup` subcommand
- Random locally-administered MAC (prefix `02:`) when --mac not passed
- `--user-data`: validate file exists/readable, warn if not `#cloud-config`, merge SSH key in memory

**Section 4 — `host` revised**
- `host prune`: warn, confirm (--force skips), refuse if VMs running, tear down all networking, update snapshot
- `host init` idempotency: rerunning on already-initialised host exits cleanly

**Section 5 — `help` consistency**
- `<cmd> help`, `<cmd> -h`, `<cmd> --help`, `help <cmd>` all produce identical output at every level

**Section 6 — `API.md` developer documentation**
- `firecracker-manager/docs/API.md` per full spec in phase 4

**Section 7 — Distribution**
- `pyproject.toml`: hatchling build backend, all runtime deps pinned, dev extras including pyinstaller
- pipx/uvx compatible (no import-time side effects requiring root)
- `release.yml` updated: PyInstaller --onefile on ubuntu-22.04 and ubuntu-24.04
- `README.md` "Installation" section covering binary, pip, pipx, uvx, build-from-source

**Section 8 — `vm create` final flag set documented and implemented**

### Out of Scope

- New features beyond what is specified in `python-cli-phase-4.md`
- Refactoring of Phase 1-3 code unless required to satisfy a Phase 4 requirement
- Integration tests (require system resources / root access)
- Windows or macOS support

## Technical Constraints

- Use existing Typer + uv + ruff + mypy stack; no new frameworks
- No compiled extensions in Python source
- Existing CLI interface must remain backward-compatible
- All Python work lives under `firecracker-manager/`
- Commands run from inside `firecracker-manager/` using `uv run`

## Dependencies

- Builds directly on the complete Phase 1-3 implementation (all marked Complete)
- GitHub Actions `release.yml` workflow (introduced Phase 2) must be updated for PyInstaller builds
- No external service dependencies

## Methodology: traditional

## Complexity: complex
