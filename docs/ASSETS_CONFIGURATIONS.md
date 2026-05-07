# Assets Configuration Reference

This document describes the three bundled YAML files that drive asset management in
`mvm`, how each field is interpreted at runtime, and how to extend them.

All three files live under `src/mvmctl/assets/` and are packaged into the installed
wheel. They are read-only at runtime — user overrides are resolved from the SQLite
database (`~/.cache/mvmctl/mvmdb.db`), runtime config (`~/.config/mvmctl/config.json`),
and `MVM_*` environment variables, not by editing
these files directly. Images and
kernels remain as `images.yaml` and `kernels.yaml` respectively.

---

## Table of Contents

- [images.yaml](#imagesyaml)
- [kernels.yaml](#kernelsyaml)
- [Runtime defaults (constants.py)](#runtime-defaults-constantspy)
- [Configuration priority](#configuration-priority)
- [Adding a new image](#adding-a-new-image)
- [Constants reference](#constants-reference)

---

## images.yaml

**Path:** `src/mvmctl/assets/images.yaml`
**Consumed by:** `mvm image pull`, `mvm image ls --remote`, `core.image._service.ImageService`

Defines the catalogue of rootfs images available via `mvm image pull <id>`.

### Structure

```yaml
images:
  - id: <string>            # unique identifier used on the CLI
    type: <string>          # OS family (ubuntu, debian, alpine, etc.)
    version: <string>       # OS version string
    name: <string>          # human-readable display name
    source: <url>           # download URL; may be a template (see below)
    format: <string>        # source file format (see Format types)
    sha256: <hex|null>      # expected SHA-256 of the downloaded file, or null
    sha256_url: <url|null>  # (informational) upstream checksum URL
    list_url_template: <url|null>  # S3 listing template (for template sources only)
```

### Field reference

| Field | Required | Description |
|-------|----------|-------------|
| `id` | ✅ | Short identifier used on the CLI (`mvm image pull <id>`). Must be unique across all entries. |
| `name` | ✅ | Human-readable label shown in `mvm image ls`. |
| `source` | ✅ | Download URL. Either a concrete URL or a **template URL** (see [Template sources](#template-sources)). |
| `format` | ✅ | Format of the downloaded file. See [Format types](#format-types). |
| `type` | — | OS family / distribution type (`ubuntu`, `debian`, `alpine`, `archlinux`, `firecracker`). |
| `version` | — | OS version string (`24.04`, `12`, `3.21`, `latest`). |
| `sha256` | — | Exact SHA-256 hex digest of the downloaded file. When `null`, the checksum is fetched from `sha256_url`. When set, the download is rejected if the digest does not match. |
| `sha256_url` | — | Upstream checksum URL. When `sha256` is `null`, the fetch logic downloads this file to extract the matching digest. |
| `list_url_template` | — | S3 listing URL template for template-based sources (see [Template sources](#template-sources)). |

### SHA-256 semantics

```
sha256: <hex>    → download verified against this exact digest
sha256: null     → checksum fetched from `sha256_url` (sidecar file)
```

Setting `sha256: null` requires a valid `sha256_url` field. The fetch logic will
download the checksum file and extract the matching digest for the downloaded asset.

### Format types

| `format` value | Source file | Conversion |
|----------------|-------------|------------|
| `qcow2` | QEMU copy-on-write v2 image | `qemu-img convert` → raw → root partition extracted |
| `tar-rootfs` | Tar archive of a root filesystem | `mkfs.ext4` + `tar -xf` into a fresh image |
| `raw` | Raw disk image | Root partition extracted directly |
| `squashfs` | SquashFS filesystem image | `unsquashfs` → `mkfs.ext4 -d` |
| `vhd` | Microsoft VHD image | `qemu-img convert` → raw → root partition extracted |

### Template sources

When the `source` URL contains `{…}` placeholder tokens, `mvm` treats it as a
**template** and resolves it dynamically at fetch time instead of downloading it
directly.

```yaml
source: "https://spec.ccfc.min/firecracker-ci/{ci_version}/{arch}/ubuntu-{ubuntu_version}.squashfs"
```

Resolution works as follows:

1. The active Firecracker CI version is read from the default binary entry in
   `~/.cache/mvmctl/metadata.json` (`binaries.*.ci_version` where `is_default=1`),
falling back to `DEFAULT_FIRECRACKER_CI_VERSION` (standalone constant in `constants.py`).
2. The host architecture is detected via `platform.machine()`, falling back to
   `defaults.kernel.arch` in `OVERRIDABLE_DEFAULTS`.
3. The `list_url_template` field in the image's YAML entry is queried to list all
   matching S3 objects for that version and architecture.
4. The latest matching entry (lexicographic sort) is selected as the concrete download
   URL, combining the S3 base with the key from the listing.

Any image entry whose `source` contains `{` triggers this path automatically — no
special `id` value or `format` is required.

### Current images

| ID | Name | Format | Type |
|----|------|--------|------|
| `ubuntu-24.04` | Ubuntu 24.04 LTS (Noble) | `tar-rootfs` | ubuntu |
| `ubuntu-24.04-minimal` | Ubuntu 24.04 Minimal | `tar-rootfs` | ubuntu |
| `ubuntu-22.04` | Ubuntu 22.04 LTS (Jammy) | `tar-rootfs` | ubuntu |
| `archlinux` | Arch Linux | `qcow2` | archlinux |
| `debian-bookworm` | Debian 12 (Bookworm) | `qcow2` | debian |
| `ubuntu-fc` | Ubuntu (Firecracker CI) | `squashfs` | firecracker |
| `alpine-3.21` | Alpine Linux 3.21 | `vhd` | alpine |

---

## kernels.yaml

**Path:** `src/mvmctl/assets/kernels.yaml`
**Consumed by:** `mvm kernel pull` (build pipeline or direct download)

Defines the default parameters for the official upstream kernel build workflow and the Firecracker CI kernel download workflow.

### Structure

```yaml
kernel-official:
  type: official               # kernel type (official or firecracker)
  version: <string>          # kernel version to fetch (e.g. "6.19.9")
  source: <url>              # tarball URL (can reference {version})
  sha256: <hex|null>         # expected digest of the tarball, or null
  sha256_url: <url|null>     # upstream checksum URL (informational)
  config_url_template: <url> # URL to fetch the base config from
  config_fragments:          # list of config overlay files or URLs to apply
    - <path_or_url>
  output_name: <string>      # filename for the built vmlinux
  build_dir: <path>          # temporary directory used during compilation
  parallel_jobs: <int|null>  # build parallelism; null = use FALLBACK_KERNEL_BUILD_JOBS
  enabled_configs:           # kernel options to enable (--enable)
    - <CONFIG_OPTION>
  disabled_configs:          # kernel options to disable (--disable)
    - <CONFIG_OPTION>
  set_val_configs:           # kernel options to set to a specific value (--set-val)
    - option: <CONFIG_OPTION>
      value: <string>
  required_settings:         # settings that MUST be =y after build; missing ones trigger a prompt
    - <CONFIG_OPTION=y>

kernel-firecracker:
  type: firecracker
  version: <string>
  source: <url>
  list_url_template: <url>   # S3 listing URL template for dynamically resolving the latest binary
  config_url_template: <url> # Optional base config template
  output_name: <string>
  build_dir: <path>
  sha256: <hex|null>
  sha256_url: <url|null>
  config_fragments: []
  parallel_jobs: null
  enabled_configs: []
  disabled_configs: []
  set_val_configs: []
  required_settings: []
```

### Field reference

| Field | Description |
|-------|-------------|
| `type` | Whether this entry represents an `official` (built from source) or `firecracker` (pre-built) kernel. |
| `version` | Kernel version string. Overridden by `--version` on the CLI. |
| `source` | Tarball download URL or S3 base URL. |
| `list_url_template` | S3 listing URL template for Firecracker CI kernels; placeholders: `{ci_version}`, `{arch}`, `{version}`. |
| `config_url_template` | URL template to download the base `.config` file. |
| `sha256` / `sha256_url` | Same semantics as [images.yaml SHA-256](#sha-256-semantics). |
| `config_fragments` | Paths or URLs to additional kernel config files merged on top of the base config. |
| `output_name` | Base filename for the compiled or downloaded `vmlinux` binary in the kernels cache. |
| `build_dir` | Working directory for the kernel compilation. Cleaned up automatically unless `--keep-build-dir` is passed. |
| `parallel_jobs` | `make -j` value. `null` defers to `defaults.kernel.build_jobs` (default: `None` = all available cores via `os.cpu_count()`). |
| `enabled_configs` | List of kernel `CONFIG_*` options passed to `scripts/config --enable`. |
| `disabled_configs` | List of kernel `CONFIG_*` options passed to `scripts/config --disable`. |
| `set_val_configs` | List of `{option, value}` pairs passed to `scripts/config --set-val`. |
| `required_settings` | List of `CONFIG_OPTION=y` strings that must be present in `.config` after the build. |

---

## Runtime defaults (constants.py)

**Location:** `src/mvmctl/constants.py` — `OVERRIDABLE_DEFAULTS` dict

This dict is the **single authoritative source** for all built-in defaults. Hardcoded
values anywhere else in the codebase are a bug.

### Structure overview

```
defaults.vm:            Default vCPU count, RAM, SSH user, boot args, LSM flags, etc.
defaults.network:       Default bridge name, CIDR, NAT enabled
defaults.image:         Default architecture
defaults.kernel:        Default kernel version, architecture, build_jobs
defaults.firecracker:   Log filenames, socket filenames, log level
defaults.cloudinit:     ISO name, nocloud-net port range
defaults.binary:        Remote version limit for bin ls
settings.vm:            Log lines, log follow, max_vms
```

Additional (non-dict) constants are defined inline for HTTP timeouts, URLs, file permissions,
and other fixed values.

### `defaults.kernel`

| Key | Default | Description |
|-----|---------|-------------|
| `version` | `6.19.9` | Default kernel version |
| `arch` | `x86_64` | Default architecture |
| `build_jobs` | `None` | Parallel compilation jobs (`None` = all available cores via `os.cpu_count()`) |

### Related constants

| Constant | Source | Description |
|----------|--------|-------------|
| `DEFAULT_FIRECRACKER_CI_VERSION` | `constants.py` (standalone constant) | CI version used when config lookup fails — resolves to `v1.15` at runtime (standalone constant in `constants.py`) |

---

## Configuration priority

Values are resolved in this order, from lowest to highest precedence:

```
1. `OVERRIDABLE_DEFAULTS` dict in `constants.py` (fallback defaults — these are the floor)
2. SQLite database (`~/.cache/mvmctl/mvmdb.db`) — canonical store for asset defaults (`is_default` markers) and runtime state
3. Runtime config file: `~/.config/mvmctl/config.json` for user overrides  
4. `MVM_*` environment variables (e.g. MVM_CACHE_DIR, MVM_KERNEL)
5. CLI flags (e.g. --out, --force, --arch)
```

`images.yaml` and `kernels.yaml` define the available asset catalogue and are not part
of this priority chain. They cannot be overridden at runtime — to use a different image
source, add it to `images.yaml` and reinstall, or use `mvm image import` for local files.

---

## Adding a new image

To register a new rootfs image that can be fetched via `mvm image pull`:

**1. Append an entry to `src/mvmctl/assets/images.yaml`:**

```yaml
- id: fedora-40                              # must be unique, no spaces
  type: fedora                               # OS family
  version: "40"                              # OS version
  name: "Fedora 40 (Cloud)"
  source: https://example.com/fedora-40-cloudimg.qcow2
  format: qcow2
  sha256: null                               # null = checksum fetched from sha256_url
  sha256_url: https://example.com/fedora-40-CHECKSUM
```

Set `sha256` to the exact SHA-256 hex digest of the file if you want integrity
verification on every download. Set it to `null` for rolling-release URLs where the
digest changes each build.

**2. For template-based sources (dynamic resolution via S3 listing):**

If the real download URL is only known at runtime (for example, because the exact
version number is embedded in the filename and changes with each Firecracker release),
use `{placeholder}` tokens in `source`:

```yaml
- id: my-dynamic-image
  name: "My Dynamic Image"
  source: "https://s3.example.com/{ci_version}/{arch}/my-image.squashfs"
  list_url_template: "http://s3.example.com/?prefix={ci_version}/{arch}/my-image&list-type=2"
  format: squashfs
  sha256: null
  sha256_url: null
```

Any `source` value containing `{` is automatically treated as a template and passed to
the dynamic resolver at fetch time. Make sure `list_url_template` is provided for
template-based entries so the dynamic resolver can find the correct S3 listing endpoint.

**3. Verify the new entry is visible:**

```bash
uv run mvm image ls --remote
```

The new ID should appear in the table. Fetch it to confirm end-to-end:

```bash
uv run mvm image pull <your-new-id>
```

---

## Constants reference

`constants.py` stores runtime defaults in the `OVERRIDABLE_DEFAULTS` dict and
exposes both inline constants and lazily-resolved values. The table below lists
the constants relevant to asset management.

| Access pattern | Config path | Description |
|----------------|-------------|-------------|
| `get_default("defaults.kernel", "version")` | `defaults.kernel.version` | Default version for `mvm kernel pull --type official` |
| `get_default("defaults.kernel", "arch")` | `defaults.kernel.arch` | Default architecture for kernel operations |
| `get_default("defaults.image", "arch")` | `defaults.image.arch` | Default architecture for image operations |
| `DEFAULT_FIRECRACKER_CI_VERSION` (standalone constant) | `constants.py` (Section 3 — VM constants) | CI version used when config lookup fails |
| `SUPPORTED_IMAGE_EXTENSIONS` (standalone constant) | `constants.py` (Section 5 — Image & rootfs processing) | File extensions scanned for cached images |
| `IMAGE_IMPORT_FORMAT_MAP` (standalone constant) | `constants.py` (Section 5) | Extension → format auto-detection table |
| `HTTP_TIMEOUT_KERNEL_DOWNLOAD_S` (standalone constant) | `constants.py` (Section 10 — HTTP / download) | Timeout (seconds) for kernel tarball download |
| `HTTP_TIMEOUT_KERNEL_CONFIG_S` (standalone constant) | `constants.py` (Section 10) | Timeout for kernel config download |
| `HTTP_TIMEOUT_SHA256_FETCH_S` (standalone constant) | `constants.py` (Section 10) | Timeout for SHA-256 checksum fetch |
| `FIRECRACKER_GITHUB_RELEASES_API_URL` (standalone constant) | `constants.py` (Section 10) | GitHub API endpoint for Firecracker releases |
| `FIRECRACKER_GITHUB_DOWNLOAD_URL` (standalone constant) | `constants.py` (Section 10) | Base URL for Firecracker release assets |

> **Note:** The kernel config lists (`enabled_configs`, `disabled_configs`, `set_val_configs`,
> `required_settings`) are defined per-kernel in `kernels.yaml`, not as module-level constants.
> Load them at runtime via `ImageService.load_available_images()` or by reading `kernels.yaml`
> through `AssetManager`.

---

*See also: [custom-kernel.md](custom-kernel.md) for the kernel build workflow,
[API.md](API.md) for the Python API reference.*
