# Firecracker Manager — Project Requirements

## Background and Goal

The repository currently contains a set of bash scripts across `assets/`, `multi-vm/`,
`single-vm/`, `custom-images/`, `environment_setup.sh`, and `ssh/`. These were written as
a proof of concept to validate the workflow of managing Firecracker microVMs — downloading
kernels and rootfs images, setting up networking, launching VMs, injecting cloud-init, and
tearing everything down cleanly. They work, but they are not maintainable, not testable,
and not portable.

The goal is to take everything those scripts do and rebuild it as a proper Python CLI
application inside the `firecracker-manager/` folder. This application must be:

- **Standalone** — no dependency on the bash scripts. They are reference material only.
  Read them to understand what each step does (e.g. how the tap device is created, how
  cloud-init is embedded into the rootfs, how the Firecracker JSON config is structured),
  then implement the equivalent logic in Python.
- **Independently installable** — the project must be buildable into a single CLI binary
  that can be distributed and run on any machine with Python 3.13. It must not assume the
  bash scripts or any repo-specific directory structure exists at runtime.
- **Multi-VM only** — the single-vm bash scripts exist but that use case is not being
  carried forward. The Python CLI targets multi-VM management exclusively.

---

## Project Identity and Build Flags

The project name (e.g. `mvm`) must be defined once in a single configuration file at the
repository root — the natural place for this is `pyproject.toml` under `[project] name`.
It must never be hardcoded anywhere else in the source code. Every place the project name
is needed at runtime — CLI binary name, environment variable prefixes, cache directory
name, network device prefixes, config filename — must derive it from that single source.

The recommended approach is to expose the project name as a package-level constant
generated at build time (e.g. via a `_version.py` or `constants.py` that is written by
the build system or a `hatch` / `flit` hook). This is sometimes called a "build flag"
pattern. The point is: rename the project in `pyproject.toml` and everything else updates
automatically — no grep-and-replace across the codebase.

**Concrete consequences of this rule:**

- Cache directory: `~/.cache/<project-name>/`
- Environment variable prefix: `<PROJECT_NAME>_` (uppercased), e.g. `FCM_CACHE_DIR`
- Network device names: `<project-name>-br0`, `<project-name>-tap0`, `<project-name>-tun0`
- Default config filename: `<project-name>.yaml`, e.g. `mvm.yaml`
- CLI binary name: matches the project name

---

## Scope Boundaries

**In scope:**
- Multi-VM lifecycle: create, list, delete, ssh, logs
- Asset management: Firecracker/jailer binaries, kernels (minimal and upstream), rootfs
  images
- Automatic network setup and teardown tied to VM lifecycle
- YAML config file support
- A Python API layer usable independently of the CLI
- Full test suite, CI workflow, documentation

**Explicitly out of scope:**
- Single-VM functionality — do not port it, do not add it
- Any wrapper around or dependency on the existing bash scripts at runtime

---

## Cache Directory Layout

All runtime state, downloaded assets, and generated files must live under a single cache
root. This keeps the user's system clean and makes it trivial to nuke everything if needed.

**Default root:** `~/.cache/<project-name>/`
**Override:** `<PROJECT_NAME>_CACHE_DIR` environment variable

The directory structure inside the cache root must be:

```
<cache-root>/
  bin/
    firecracker-v1.x.x        # versioned firecracker binary
    jailer-v1.x.x             # versioned jailer binary
  kernels/
    minimal-v6.x.x            # official prebuilt Firecracker kernel
    upstream-<hash-or-tag>    # custom-built kernel
  images/
    ubuntu-cloud-24.04.ext4
    firecracker-ubuntu.ext4
    arch.btrfs
    debian-bookworm.ext4
  keys/
    <vm-name>.id_rsa
    <vm-name>.id_rsa.pub
  vms/
    <vm-name>/
      firecracker.json        # generated VM config
      firecracker.pid         # PID of running process
      firecracker.socket      # API socket (if enabled)
      console.log             # serial console output
      cloud-init/             # generated cloud-init seed files
```

The `bin/` subdirectory holds only binaries (firecracker, jailer). The `vms/` subdirectory
holds only per-VM runtime state. Kernels and images are shared across VMs. This separation
is intentional and must be preserved — do not collapse these into a flat structure.

---

## CLI Design

### Conventions

Follow Docker CLI conventions. This is the most widely recognised pattern for infrastructure
tooling and reduces the learning curve for new users:

- Use `ls` as the primary listing subcommand (with `list` as an alias)
- Use `rm` for removal (with `remove` and `delete` as aliases)
- Use `create` for creation
- Use noun-first grouping: `vm ls`, `asset kernel fetch`, `asset bin ls`
- Flags use `--long-form` with `-s` short forms for the most common ones
- Output defaults to human-readable tables; add `--json` flag on every listing command for
  machine-readable output (important for the API and for TUI/GUI consumers)
- Exit codes follow Unix convention: 0 for success, non-zero for errors

### Command Groups

#### `vm` — Virtual machine lifecycle

```
vm ls                           List all VMs (name, status, IP, PID, kernel, image)
vm create                       Create and start a new VM
vm rm <name>                    Stop and permanently delete a VM and its cache directory
vm ssh <name|ip>                Open an SSH session; --user flag for login user
vm logs <name|ip>               Print or stream the serial console log; --follow/-f to tail
```

`vm create` accepts the following flags. All are optional when a config file is present:

```
--name <string>         VM name; also used as the guest hostname and cache subdirectory name
--kernel <name|path>    Kernel to boot; name refers to a cached kernel, path is a raw file
--image <name|path>     Rootfs image; same name-or-path resolution as --kernel
--ssh-key <path>        Path to SSH public key to inject via cloud-init
--vcpus <int>           Number of vCPUs (default: 2)
--memory <int>          Memory in MiB (default: 2048)
--ip <cidr>             Guest IP address with prefix length, e.g. 10.10.0.2/30
--enable-socket         Enable the Firecracker API socket (default: off)
--enable-pci            Enable PCI device support (default: off; needed by some distros)
```

#### `asset` — Asset management

Assets are split into three sub-groups: `kernel`, `image`, and `bin`. Each follows the
same `ls` / `fetch` / `rm` pattern for consistency.

```
asset kernel ls                       List cached kernels; mark the active one
asset kernel fetch                    Download the official Firecracker minimal kernel
asset kernel build                    Build a custom upstream kernel (interactive or from flags)
asset kernel config <flag> <on|off>   Enable or disable a single kernel .config flag before build
asset kernel rm <name>                Remove a cached kernel

asset image ls                        List cached rootfs images
asset image fetch <type>              Download a rootfs image; <type> is one of the supported values below
asset image rm <name>                 Remove a cached image

asset bin ls                          List Firecracker/jailer versions: remote available + local installed;
                                        mark the currently active version with a checkmark
asset bin fetch <version>             Download a specific Firecracker + jailer version pair
asset bin use <version>               Set a downloaded version as the active binary
asset bin rm <version>                Remove a locally cached binary version

asset cache clear                     Remove all cached assets (bin/, kernels/, images/)
                                        Does NOT touch vms/ — VM runtime state is managed separately
```

**Supported image types for `asset image fetch`:**

| Type | Description |
|---|---|
| `ubuntu-cloud` | Official Ubuntu cloud image (configurable release, default: latest LTS) |
| `firecracker-ubuntu` | Firecracker's own minimal Ubuntu image — smaller, faster to boot |
| `arch` | Arch Linux cloud image |
| `debian` | Debian cloud image (configurable release, default: bookworm) |

**Kernel types — keep these two completely distinct throughout the codebase:**

| Type | Description |
|---|---|
| `minimal` | Official prebuilt Firecracker kernel binary. Downloaded directly from the Firecracker GitHub releases. No compilation required. Recommended for most users. |
| `upstream` | Custom kernel built from source. The user can enable/disable individual kernel config flags before building via `asset kernel config`. Intended for users who need features not present in the minimal kernel (e.g. specific filesystems, eBPF, etc.). |

#### `host` — Host machine configuration

The `host` command group manages the one-time system-level setup that the host machine
needs to run Firecracker VMs. This is the Python equivalent of `environment_setup.sh` —
it enables KVM access, loads kernel modules, configures sysctl parameters, and verifies
that all required system packages are present.

Unlike the automatic per-VM networking managed by `api/network.py`, host initialisation
touches system-wide settings that persist across reboots and affect the machine globally.
It therefore requires explicit user invocation and explicit user consent to reverse. The
host state (what was changed, what the original values were) is snapshotted to the cache
folder before any modification is made, so it can be fully restored later.

```
host init       Apply all required host-level configuration changes so Firecracker VMs
                  can be created and run. Reads the current state of each setting, saves a
                  snapshot of the pre-init state to <cache-root>/host/state.json, then
                  applies each change. Idempotent: running it twice is safe and produces
                  no duplicate changes. Prints a summary of every change made and skips
                  anything already correctly configured.

host ls         Show the current host configuration state: each setting that init manages,
                  its current value, the original value recorded at init time, and whether
                  it is currently in the expected state. Useful for auditing or debugging
                  a host that may have been partially modified externally.

host restore    Revert all changes made by host init, restoring each setting to the value
                  recorded in the pre-init snapshot. Requires that host init has been run
                  at least once (snapshot must exist). Prints what is being restored and
                  warns if any setting has drifted from the expected post-init value.
```

**What `host init` must configure** (derived from `environment_setup.sh`):

- Verify `/dev/kvm` exists and is accessible by the current user; print a clear error if
  not, explaining how to add the user to the `kvm` group
- Verify all required system binaries are installed: `ip`, `iptables`, `mkisofs`, and any
  others identified in the bash scripts
- Enable global IP forwarding: `net.ipv4.ip_forward = 1` via sysctl
- Persist the sysctl change to `/etc/sysctl.d/<project-name>.conf` so it survives reboot
- Load any kernel modules required by Firecracker (e.g. `kvm`, `kvm_intel` or `kvm_amd`)
  if not already loaded

**State snapshot format** — stored at `<cache-root>/host/state.json`:

```json
{
  "init_timestamp": "2025-01-01T00:00:00Z",
  "changes": [
    {
      "setting": "net.ipv4.ip_forward",
      "original_value": "0",
      "applied_value": "1",
      "mechanism": "sysctl"
    },
    {
      "setting": "sysctl_persist_file",
      "original_value": null,
      "applied_value": "/etc/sysctl.d/<project-name>.conf",
      "mechanism": "file_create"
    }
  ]
}
```

This snapshot is the source of truth for `host restore`. If the file does not exist,
`host restore` must fail with a clear error explaining that `host init` has not been run.

---

## Configuration File

The CLI looks for a YAML config file to set default values for all operations. This avoids
requiring the user to repeat the same flags on every command.

**Resolution order (highest to lowest priority):**
1. Explicit CLI flag
2. Environment variable (e.g. `FCM_DEFAULT_IMAGE`)
3. Config file value
4. Built-in default

**Default lookup path:** `./<project-name>.yaml` in the current working directory.
**Override path:** `<PROJECT_NAME>_CONFIG` environment variable.

The config file covers VM defaults, global asset preferences, networking, and LSM flags.
The `lsm_flags` value (previously hardcoded in `setup.sh` and later moved to `config.env`)
must live here so users can customise the Linux Security Module stack without touching
source code.

Here is a representative example:

```yaml
# Default assets used when creating a VM without explicit flags
defaults:
  kernel: minimal           # "minimal", "upstream", or a file path
  image: firecracker-ubuntu # image type name or a file path
  ssh_key: ~/.ssh/id_rsa
  vcpus: 2
  memory: 2048              # MiB

# Networking
network:
  guest_ip_range: 10.10.0.0/24   # Pool from which guest IPs are auto-allocated
  host_bridge: <project-name>-br0
  mask: 255.255.255.252

# Firecracker guest kernel boot parameters
boot:
  lsm_flags: "landlock,lockdown,yama,integrity,selinux,bpf"
  extra_boot_args: ""

# Firecracker runtime settings
firecracker:
  enable_socket: false
  enable_pci: false
```

---

## Networking

The bash proof-of-concept requires the user to manually run `environment_setup.sh` before
creating any VM. This manual step is eliminated entirely in the Python CLI — networking is
managed automatically as a side effect of the VM lifecycle.

**Rules:**
- When the **first VM** is created: create the bridge device, enable IP forwarding, set up
  NAT via iptables/nftables, and create a tap device for that VM.
- When **each additional VM** is created: create a new tap device and attach it to the
  existing bridge.
- When **a VM is deleted**: remove its tap device from the bridge.
- When the **last VM is deleted**: tear down the bridge, flush all NAT rules added by this
  tool, and restore the host network to the state it was in before the first VM was
  created. Do not flush rules that were not added by this tool.
- All device names use the project-name prefix: `<project-name>-br0`,
  `<project-name>-tap<n>`.
- Guest IP addresses should be auto-allocated from the `guest_ip_range` defined in the
  config, so the user does not need to manually assign IPs unless they want to override.
- The network setup/teardown logic must live in `api/network.py` and be callable
  independently of the CLI, so it can be tested and reused by TUI/GUI consumers.

---

## Internal Python API

Every operation the CLI exposes must also be available as a regular importable Python
function in the `api/` module. The CLI commands are thin wrappers: they parse arguments,
call the API function, and format the result for display. They contain no business logic
themselves.

This design is intentional and important: it means a TUI (e.g. Textual), a GUI (e.g.
PyQt), or an HTTP service can be built on top without touching the CLI code. It also makes
the logic straightforwardly unit-testable.

**Recommended return types:** use dataclasses or Pydantic models for all structured return
values (not raw dicts or tuples). This makes the API self-documenting and type-safe.

**Recommended module layout:**

```
firecracker_manager/
  api/
    assets.py     # fetch_kernel(), build_kernel(), configure_kernel_flag(),
                  # fetch_image(), fetch_binary(), list_binaries(), set_active_binary(), ...
    vms.py        # create_vm(), delete_vm(), list_vms(), ssh_vm(), get_logs(), ...
    network.py    # setup_network(), teardown_network(), allocate_ip(), release_ip(), ...
  cli/
    asset.py      # Typer/Click commands — thin wrappers around api/assets.py
    vm.py         # Typer/Click commands — thin wrappers around api/vms.py
    main.py       # Entry point, registers all command groups
  models.py       # Shared dataclasses / Pydantic models: VMConfig, KernelInfo, ImageInfo, ...
  config.py       # YAML config loading, env var resolution, precedence logic
  constants.py    # Project name (injected at build time), default paths, device name helpers
  exceptions.py   # Typed exception hierarchy: FCMError, VMNotFoundError, AssetNotFoundError, ...
```

All user-facing errors must be raised as typed exceptions from `exceptions.py`, never as
bare `Exception` or `SystemExit` from inside the API layer. The CLI layer is responsible
for catching these and formatting them into user-friendly messages. This separation is
what makes the API usable outside the CLI context.

---

## Testing

Testing is mandatory. The application touches the filesystem, subprocesses, and kernel
networking — all of these must be properly mocked in unit tests so the suite can run in CI
without root privileges or a real Linux environment with KVM.

**Coverage target: ≥ 80%**, enforced in CI (pipeline fails below this threshold).

**What must be tested:**

- `api/assets.py`: every public function, with mocks for HTTP downloads, subprocess calls
  (kernel build), and filesystem writes
- `api/vms.py`: create, delete, list, with mocked filesystem and subprocess (firecracker
  process launch)
- `api/network.py`: setup and teardown, including the "first VM" and "last VM" edge cases,
  with mocked `ip` and `iptables` subprocess calls
- `config.py`: YAML loading, env var override, precedence order, missing file handling,
  malformed YAML handling
- `cli/`: command parsing, flag defaults, error message formatting — use the Click/Typer
  test runner so no subprocess spawning is needed in CLI tests
- `models.py`: Pydantic validation where present
- Cache directory: path resolution, layout creation, `CACHE_DIR` env override

**Tools:**
- `pytest` as the test runner
- `pytest-cov` for coverage measurement
- `unittest.mock` or `pytest-mock` for mocking
- `tmp_path` pytest fixture for all filesystem operations — tests must never write to real
  system paths

---

## GitHub Actions CI

### `ci.yml` — runs on every push and pull request to the main branch

**Steps in order:**

1. Set up Python 3.13
2. Install the project with dev dependencies: `pip install -e ".[dev]"`
3. Lint: `ruff check .` — fail on any error
4. Type-check: `mypy firecracker_manager/` — fail on any error
5. Test: `pytest --cov=firecracker_manager --cov-fail-under=80`
6. Upload coverage report as a workflow artifact

### `release.yml` — runs on version tags (`v*.*.*`)

This workflow builds the distributable binary, attaches it to the GitHub release, and
verifies the binary works before publishing. The recommended tool for producing a
self-contained Python binary is **PyInstaller** (single-file mode via `--onefile`).
Alternatively, **Nuitka** or **shiv** are acceptable if they produce a cleaner result for
this use case — the choice must be documented in `CONTRIBUTING.md`.

**Steps in order:**

1. Set up Python 3.13
2. Install the project and build dependencies: `pip install -e ".[dev]" pyinstaller`
3. Run the full test suite — fail the release if any test fails
4. Build the binary:
   ```
   pyinstaller --onefile --name <project-name> firecracker_manager/cli/main.py
   ```
5. Smoke-test the binary: run `dist/<project-name> --version` and `dist/<project-name> --help`
   and fail the workflow if either exits non-zero
6. Create a GitHub release (if not already created from the tag) and upload the binary
   as a release asset
7. Upload the binary as a workflow artifact as well, so it can be downloaded directly
   from the Actions run without needing a formal release

**Matrix builds:** the release workflow must build the binary on both `ubuntu-22.04` and
`ubuntu-24.04` runners and produce a named artifact for each (e.g.
`<project-name>-linux-ubuntu22`, `<project-name>-linux-ubuntu24`). Firecracker only runs
on Linux so macOS and Windows builds are not required.

---

## Documentation

**`README.md`** — written for a first-time user who may not have used Firecracker before.
Must cover:
- What this tool does and why (one concise paragraph)
- Prerequisites: OS (Linux), Python 3.13, required system packages (`ip`, `iptables`,
  `mkisofs`, KVM device access), and any kernel modules needed
- Installation options — three paths, all documented:
  1. **Download the prebuilt binary** from the GitHub releases page (simplest, no Python
     required at runtime)
  2. **Install via pip**: `pip install <project-name>`
  3. **Build from source** (see below)
- Quickstart: download assets, initialise the host, create a VM, SSH in, delete it — in
  under 10 commands with expected output shown
- Full command reference with real usage examples for every command and flag
- Config file reference documenting every supported key and its default
- Environment variable reference
- **Building from source** section — this must be a self-contained section in `README.md`
  (not buried in `CONTRIBUTING.md`) because users who want to customise the binary or use
  a fork need this. It must cover:
  - Clone the repo
  - Install Python 3.13 and `pip install -e ".[dev]" pyinstaller`
  - Run the build: `pyinstaller --onefile --name <project-name> firecracker_manager/cli/main.py`
  - Where the output binary is located (`dist/<project-name>`)
  - How to verify the build: `dist/<project-name> --version`
  - Note that the project name comes from `pyproject.toml` and can be changed there before
    building to produce a renamed binary
- Link to `CONTRIBUTING.md` for developers who want to modify or extend the project

**`CONTRIBUTING.md`** — written for a developer extending or modifying the project:
- Dev environment setup
- How to run the test suite locally
- How the project name / build flag system works and how to change the project name
- Project structure walkthrough explaining each module's responsibility
- How to add a new CLI command
- How to add a new supported image type
- PR and code review expectations

**`LICENSE`** — MIT.

**`.gitignore`** — standard Python entries, plus: cache directories, built binaries,
generated `_version.py`, `*.pid`, `*.socket`, `*.log`, and the `vms/` runtime directory.

---

## General Engineering Constraints

- **No hardcoded strings for names, paths, or prefixes.** Everything derives from the
  project name constant or the config file.
- **Sensible defaults everywhere.** A user who runs `vm create` with no flags and a
  minimal config file should get a working VM without any additional configuration.
- **Clean error messages.** Every error shown to the user must explain what went wrong and
  what to do next. Stack traces must never be shown by default; expose them only via a
  `--debug` flag.
- **Idempotency where possible.** Running `asset kernel fetch` twice should not fail or
  re-download — check if the asset is already cached first and skip if so.
- **Consistent naming.** Command names, flag names, Python function names, and environment
  variable names must all follow the same logical pattern. A contributor should be able to
  guess any flag or function name without looking it up.
- **No silent failures.** Any subprocess call (`ip`, `iptables`, `firecracker`, kernel
  build) that fails must raise a typed exception with the stderr output attached. Never
  swallow errors with bare `except` blocks or the Python equivalent of `|| true`.
