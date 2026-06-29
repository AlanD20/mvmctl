# Contributing to mvmctl

Thanks for wanting to contribute. This guide covers everything you need to get set up and productive.

## Prerequisites

- **Go 1.26.3+** — check with `go version`
- **Linux** (x86_64 or aarch64) — Firecracker only runs on Linux with KVM
- **System packages** — see [docs/DEPENDENCIES.md](docs/DEPENDENCIES.md) for distro-specific lists
- **git**

## Development Setup

```bash
git clone https://github.com/AlanD20/mvmctl
cd mvmctl
./scripts/build.sh dev
./mvm --version
```

## Project Structure

```
cmd/mvm/main.go            # Entry point
internal/
├── cli/                   # Cobra command definitions (one file per domain)
├── core/*/                # Domain logic
│   ├── *resolver.go       # Selector resolution (name, ID prefix, IP, etc.)
│   ├── *service.go        # Business logic
│   ├── *repository.go     # Database access
│   └── *controller.go     # State management (start/stop/pause/resume)
├── lib/                   # Shared leaf utilities (db, crypto, system, version, model, logging, network, provisioner, ...)
├── infra/                 # Infrastructure helpers (constants, system, firewall)
├── service/               # Background subprocess services
pkg/
├── api/                   # API layer — input validation, cross-domain orchestration
├── api/inputs/            # Validate() + Resolve() per operation
└── errs/                  # DomainError (single error type with Code + Class)
tests/
└── system/                # L2 system tests (one dir per domain)
scripts/                   # Build, release, test orchestration
```

Three-tier architecture: **CLI → API → Core**. CLI stays thin (arg parsing + output). API is the sole orchestrator of multiple core domains. Core domains are isolated — never import another core package.

## Running Tests

| Layer | Command | What it covers |
|-------|---------|---------------|
| L0/L1 unit | `go test ./... -count=1` | Pure functions, in-memory repos, hermetic tests |
| L2 system | See full guide below | End-to-end with real Firecracker VMs (requires KVM) |

For L2 system tests — running specific tiers, domains, preparing assets, interpreting results, and troubleshooting — follow [docs/development/HOW_TO_RUN_SYSTEM_TESTS.md](docs/development/HOW_TO_RUN_SYSTEM_TESTS.md). That doc covers the orchestrator script, prerequisite checks, the `--prepare` / `--rebuild` / `--tier` / `--push` flags, and common failure patterns.

## Code Style

This project follows standard Go conventions:

```bash
# Format
gofmt -l .    # should produce no output

# Line length (120 chars max on Go source)
golines --max-len=120 --no-reformat-tags --list-files ./internal/ ./pkg/ ./cmd/

# Vet
go vet ./...

# Tidy
go mod tidy && git diff --exit-code
```

See [docs/STANDARDS.md](docs/STANDARDS.md) for the full coding standards reference.

## Commit Conventions

Follow [Conventional Commits](https://www.conventionalcommits.org/):

```
feat: add mvm vm pause command
fix: handle missing kernel path gracefully
test: add unit tests for image converter
docs: update quick start in README
refactor: extract network helpers to internal/lib/
chore: bump dependencies
```

Keep the subject under 72 characters. Add a body if the change needs explanation.

## Pull Request Process

1. **Branch** — create from `main`: `git checkout -b feat/my-feature`
2. **Focus** — one feature or fix per PR. Keep commits focused.
3. **CI gate** — run the full gate locally before pushing:
   ```bash
   go mod tidy && git diff --exit-code
   go vet ./...
   go test ./... -count=1
   golines --max-len=120 --no-reformat-tags --list-files ./internal/ ./pkg/ ./cmd/
   ```
4. **Layering check** — read [CONTEXT.md](CONTEXT.md) for architecture rules:
   - Core domains never import other core packages
   - Validation lives in `pkg/api/inputs/`, not in Service/Controller
   - ALL subprocess calls through `system.DefaultRunner.Run()` / `system.DefaultRunner.Stream()`
   - Controller = state management only (start/stop/pause/resume) — no create/remove
5. **Open a PR** against `main`. Describe *why*, not just *what*.

> Most CI failures come from `golines` line-length violations — run it locally first.

## Design Decisions

When proposing a change that touches architecture boundaries, review the relevant ADRs in [docs/adr/](docs/adr/). Hard-to-reverse decisions (three-tier architecture, error handling model, test strategy) are documented there with trade-offs.

Key constraints:
- **No `reflect`**, no `goto` — banned unless approved via ADR.
- **Error handling** uses `pkg/errs.DomainError` — single error type with Code + Class. No multiple error types.
- **Context propagation** — every repository method, every infrastructure function with side effects takes `ctx context.Context` as its first parameter.
- **Single binary** — all Go code compiles into one binary. Background services (console relay, nocloud server) are spawned as subprocesses from that binary.

## Questions?

Open an issue if something in this guide is unclear or out of date.
