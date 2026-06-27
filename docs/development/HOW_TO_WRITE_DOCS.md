# How to Write Docs

## Mandatory Rules for All Docs

### Rule A: Every Doc Must Have a Table of Contents

Every document under `docs/` with more than one section **MUST** have a `## Table of Contents`
section after the introductory paragraph, with bullet links to every `##` heading in the file.
Use GitHub-style anchors (lowercase, spaces to hyphens, strip non-alphanumeric).

```markdown
## Table of Contents

- [Section One](#section-one)
- [Section Two](#section-two)
```

This lets readers navigate without scrolling through hundreds of lines.

### Rule B: Single Source of Truth — No Duplication

**Never duplicate information that already exists in another document.** Duplication creates
stale information — updating one copy but forgetting the other is the #1 source of doc rot.

Instead of copying content from another doc, **link to it**:

```markdown
See [DEPENDENCIES.md](DEPENDENCIES.md) for the required build packages per distribution.
```

Correct: Reference existing docs by link. Keep package tables in `DEPENDENCIES.md` only,
kernel build commands in `KERNEL.md` only, etc.

Wrong: Copying the apt-get install command from DEPENDENCIES.md into TROUBLESHOOTING.md
because it's convenient — now there are two places to update when the package name changes.

**Exceptions:**
- Troubleshooting sections may inline commands that are workarounds for automated processes.
- The public site (`mvmctl.com`) may reference docs in the repo but should not inline
  large sections from them.

---

When an agent is asked to audit and update all documentation, follow this exact protocol:

### 1. Start with parallel exploration

Spawn multiple subagent instances (e.g., using the task system) for parallel exploration — each responsible for a group of docs. These are ephemeral research tasks, not separately defined agent instruction files. Divide the work by doc category:

| Subagent | Scope |
|----------|-------|
| Explore 1 | Root docs (CONTEXT.md, AGENTS.md, README.md, CHANGELOG.md) |
| Explore 2 | `docs/` folder (PROJECT_ARCHITECTURE.md, REFERENCES.md, TROUBLESHOOTING.md, RUNTIME.md, DEPENDENCIES.md, ASSETS_CONFIGURATIONS.md, KERNEL.md, RELEASE.md) |
| Explore 3 | ADR docs (all docs/adr/ files) |
| Explore 4 | Improvement, implementation, development, and optimization docs |
| Explore 5 | Agent instruction files (`.opencode/agent/*.md`) |
| Explore 6 | Public site (`mvmctl.com`) — the path is dynamic; ask the user or use the path they provide |

Each explore subagent instance must:
1. Read ALL the doc files in its scope
2. Read the relevant source code files to verify every claim
3. Return a comprehensive per-file report listing: accurate claims, outdated/wrong claims, missing content, content to remove

### 2. Spawn engineer agents to apply fixes

Based on the explore findings, spawn multiple `engineer` agents in parallel — each targeting a specific set of files — to fix every inaccuracy found.

### 3. Verification

After all engineer agents complete:
- Spot-check the most critical files (README.md, CONTEXT.md, public site)
- For the public site (`mvmctl.com`), run the build command to verify no errors

### Core Principle

**100% line-by-line accuracy.** Every claim, code example, flag name, and description must match the current codebase. If something is missing, add it. If something is outdated, update it. If something is no longer applicable, remove it entirely.

Spawning agents in parallel is not optional — it's the only way to cover the full documentation surface before context windows expire.

---

## Audience Classification

> This section describes the current audience structure implemented on the public site at [mvmctl.com](https://mvmctl.com).

The project has two distinct documentation audiences:

| Audience | Where they read | What they need |
|----------|----------------|----------------|
| **Users** | `mvmctl.com` (public site) | Install, quick start, essential commands, full command reference |
| **Contributors** | `docs/` in the repo, `CONTEXT.md`, `AGENTS.md` | Architecture, build system, domain internals, test strategy, agent instructions |

**Rules:**
- The public site (`mvmctl.com`) is for **users only**.
- Contributors must go into the repo's `docs/` directory to understand internals.
- Never inline contributor-level content on the public site. Link to it instead.

---

## Site Structure

> These files exist and are actively maintained at `mvmctl.com/src/content/site/`. This section accurately describes their current structure.

### `landing.ts` — Marketing page
- Hero with tagline, CTA, command preview
- Feature cards (3-4 max)
- Install methods (one-liner each)
- Links to docs
- **No command details, no architecture, no troubleshooting**

### `docs.ts` — User guide
- Overview
- Prerequisites
- Install
- Host Initialize
- First VM
- VM create (all flags reference)
- VM Lifecycle (SSH, Console, Logs, Snapshots, Removing)
- Resource Management (Image, Kernel, Binary, SSH Key, Volume)
- Network Management
- Configuration
- Cloud-Init
- Troubleshooting

---

## Content Contract — What Goes on the Public Site

> This section defines the boundary between content that lives on the public site ([mvmctl.com](https://mvmctl.com)) vs content that belongs in the repo's `docs/` directory.

### Belongs on the site

| Category | Examples |
|----------|----------|
| What it is | Tagline, elevator pitch, feature list |
| Install | Binary, from source — one command per method |
| Prerequisites | KVM, Go version, system packages |
| Quick start | Copy-paste sequence from init → running VM |
| Essential commands | Short reference, one flag per command max |
| Full flag reference | Table of all flags per command |
| Configuration | Config file location, env vars (list only, link for details) |
| Cloud-Init | Modes, how it works, security model |
| Troubleshooting | Common errors with copy-paste fixes |

### Does NOT belong on the site

| Category | Why | Where it goes instead |
|----------|-----|----------------------|
| Internal architecture | Three-layer design, domain structure, Controller/Service/Repository/Resolver | `CONTEXT.md` |
| Build system | Go build flags, `go build`, dist/ layout | `docs/RELEASE.md`, `CONTEXT.md` |
| Sudoers/sudo internals | `PRIVILEGED_BINARIES`, `sg mvm -c`, sudoers file contents | `docs/adr/0005-sudo-privilege-architecture.md` |
| Provisioner backends | LoopMount vs GuestFS comparison, losetup/btrfs/chroot deps | `CONTEXT.md`, `docs/adr/0003-loopmount-guestfs-mutual-exclusion.md` |
| Manual sudoers config | `mvm init` handles this | No doc needed (automated) |
| Kernel build deps | Build packages for `kernel pull --type official` | `docs/KERNEL.md` |
| DB schema | SQLite tables, migrations, column layout | `CONTEXT.md` |
| Cache directory structure | `~/.cache/mvmctl/` filesystem layout | No doc needed (users don't need to know) |
| Dependency tables per distro | `apt-get` vs `pacman` package names for every internal tool | `docs/DEPENDENCIES.md` |
| Agent instructions | AGENTS.md, `.opencode/agent/*.md` | These files in the repo |

---

## Five Rules for All Public-Facing Content

### Rule 1: One Command Per Action

Never show multi-step setup when a single flag exists. If the CLI has `--default`, use it — don't show `mvm key create test` followed by `mvm key default test`. Two commands = two chances for the user to get it wrong.

Correct: `mvm key create test --default`
Wrong: `mvm key create test` then `mvm key default test`

This applies everywhere: `--default`, `--force`/`-f`, `--json`. If the flag exists, use it inline.

### Rule 2: No Internal Architecture in User-Facing Docs

The README and public website cover: **what it is, install, quick start, essential commands, troubleshooting.** That's it.

Internal architecture — three-layer design, domain structure, Controller/Service/Repository pattern, DB schema, cache directory layout, build system internals, shared infrastructure — belongs in `docs/` or `CONTEXT.md`. A user should never need to know how the code is organized to use the tool.

Correct: Link to deeper docs: "See KERNEL.md for building kernels from source."
Wrong: Inline a diagram of the three-layer architecture in the README.

### Rule 3: Commands Must Be Copy-Paste Ready

Every code block should work if the user copies it line by line into their terminal. No placeholders, no `$USER`, no assumptions about prior state.

Correct: `mvm vm create myvm --image ubuntu:24.04`
Wrong: `mvm vm create <vm-name> --image ubuntu:24.04` (placeholders in command)
Wrong: `mvm vm create $VM_NAME --image $IMAGE` (shell variables that aren't defined)

If a command requires a prerequisite, either include the prerequisite command in the block or add a clear instruction: `# First run: mvm image pull ubuntu:24.04`

### Rule 4: Lazy Linking — Link, Don't Inline

The README should be skimmable in 30 seconds. Don't dump every flag, option, and configuration detail into it. State the essential, link to the reference.

Correct: "See the full command reference."
Wrong: A 50-line flag table in the README.

Use links to `docs/` for details. The README is a front door, not an encyclopedia.

### Rule 5: Show the Happy Path First

Every section should lead with the simplest, most common operation. Advanced flags and edge cases come later.

Correct:
```
# Create a VM
mvm vm create myvm --image ubuntu:24.04
```

Wrong: Lead with `mvm vm create myvm --image ubuntu:24.04 --vcpu 4 --mem 8192 --disk-size 50G --network isolated --ip 10.0.0.50 --ssh-key mykey`

### Rule 6: If the CLI Handles It, Don't Document the Manual Way

If `mvm init` or any CLI command handles something automatically, the public site should NOT show the manual equivalent. This includes sudoers configuration, binary extraction, database setup, and provisioning backend internals. The user should never be instructed to manually edit a config file, write sudoers entries, or run system commands that the CLI manages.

Correct: "Run `mvm init` — it handles everything automatically."
Wrong: Pages of sudoers configuration, binary paths, and dependency tables explaining what happens under the hood.

The one exception: troubleshooting sections, where the manual command is the _workaround_ for an automated thing that failed.

---

## Replacement Pattern

When removing internal content from the public site, follow this pattern:

1. **Remove** the full section (code blocks, tables, descriptions).
2. **Replace** with a single sentence + link to the deeper doc.

**Before (internal):**
```markdown
### mvm sudoers configuration
The mvm binary needs passwordless sudo for provisioner operations...
%mvm ALL=(root) NOPASSWD: /home/*/.cache/mvmctl/bin/mvm
```

**After (user-facing):**
```markdown
> Internals: see the [project documentation](https://github.com/AlanD20/mvmctl) for provisioning backend details.
```

---

## Doc Accuracy Assurance

Every documentation file drifts from reality over time. The key drifts are:

| Drift type | How it happens | Prevention |
|-----------|----------------|------------|
| CLI flag changes | Subcommand `set-default` → `--default` flag, `--force` added to a command | Every PR that changes a CLI flag must update all doc files that reference it. |
| Flat slug changes | `ubuntu-24.04` → `ubuntu:24.04` | Search for the old pattern across ALL .md and .ts files. |
| Line number references | Code moves, line numbers become wrong | Never reference absolute line numbers in docs. Reference function/type names instead. |
| Package names per distro | Package renamed upstream | Keep distro package tables in `docs/DEPENDENCIES.md` only. Reference from other docs by link. |
| Go code examples | API changes | Keep examples using `mvm` CLI commands where possible. Go API patterns in `CONTEXT.md`. |

### Pre-submit checklist (for every doc change)

Before submitting any doc change, run these checks:

1. **Copy-paste every code block** into a terminal and verify it works. This catches dead flags, renamed commands, and placeholder syntax.
2. **Search for old patterns** — If you changed `set-default` subcommand to `--default` flag, grep the entire project for `set-default`. If you changed the image pull syntax, grep for the old slug.
3. **Check line number references** — If the doc says `service.go lines 200-210`, open that file and verify the lines still match the described behavior. Better: replace with function/type names.
4. **Verify links** — Every internal link (`#section`) and external link (`https://...`) must work. Run the public site build if applicable.
5. **Check for dead code examples** — If an error type is documented, verify it's still used somewhere in `internal/` or `pkg/`. If not, remove it.

### Full audit trigger

A full doc audit is needed when any of these happen:

- **CLI refactor**: Flags renamed, commands removed, positional args changed.
- **Model change**: Fields added/removed from model structs.
- **Architecture change**: New domain, backend swap, layer boundary change.
- **Pre-release**: Before any tagged release, all docs must be audited against `HEAD`.
