# Assets Configuration Reference

This document describes the three bundled YAML files that drive asset management in
`mvm`, how each field is interpreted at runtime, and how to extend them.

All three files live under `src/mvmctl/assets/` and are packaged into the installed
wheel. They are read-only at runtime — user overrides/default selections are resolved
from runtime state (`~/.cache/mvmctl/metadata.json`) and `MVM_*` environment variables,
not by editing these files directly.

---

## Table of Contents

- [images.yaml](#imagesyaml)
- [kernels.yaml](#kernelsyaml)
- [defaults.yaml](#defaultsyaml)
- [Configuration priority](#configuration-priority)
- [Adding a new image](#adding-a-new-image)
- [Constants reference](#constants-reference)

---

## images.yaml

**Path:** `src/mvmctl/assets/images.yaml`
**Consumed by:** `mvm image fetch`, `mvm image ls --remote`, `api.assets.pull_image()`

Defines the catalogue of rootfs images available via `mvm image fetch <id>`.

### Structure

```yaml
images:
  - id: <string>            # unique identifier used on the CLI
    name: <string>          # human-readable display name
    source: <url>           # download URL; may be a template (see below)
    format: <string>        # source file format (see Format types)
    convert_to: <string>    # target filesystem type written to disk
    size_mib: <int>         # image size in MiB allocated during conversion
    sha256: <hex|null>      # expected SHA-256 of the downloaded file, or null
    sha256_url: <url|null>  # (informational) upstream checksum URL
```

### Field reference

| Field | Required | Description |
|-------|----------|-------------|
| `id` | ✅ | Short identifier used on the CLI (`mvm image fetch <id>`). Must be unique across all entries. |
| `name` | ✅ | Human-readable label shown in `mvm image ls`. |
| `source` | ✅ | Download URL. Either a concrete URL or a **template URL** (see [Template sources](#template-sources)). |
| `format` | ✅ | Format of the downloaded file. See [Format types](#format-types). |
| `convert_to` | ✅ | Filesystem type produced after conversion (`ext4`, `btrfs`). Becomes the file extension of the stored image. |
| `size_mib` | ✅ | Allocated image size in MiB. Used as the `truncate` / `mkfs` size during conversion. Falls back to `image.defaults.import_size_mib` in `defaults.yaml` when omitted. |
| `sha256` | — | Exact SHA-256 hex digest of the downloaded file. When `null`, checksum verification is **skipped** and the file is downloaded without integrity checking. When set, the download is rejected if the digest does not match. |
| `sha256_url` | — | Informational field pointing to the upstream checksum page. Not used by the fetch logic — present solely as a reference for maintainers. |

### SHA-256 semantics

```
sha256: <hex>    → download verified against this exact digest
sha256: null     → no checksum check; file is downloaded as-is
```

Setting `sha256: null` is appropriate when:

- The source is a rolling release URL whose content changes over time (e.g. `latest/` paths).
- The upstream does not provide a stable per-file digest.
- A template source is used whose resolved URL changes between Firecracker releases.

### Format types

| `format` value | Source file | Conversion |
|----------------|-------------|------------|
| `qcow2` | QEMU copy-on-write v2 image | `qemu-img convert` → raw → root partition extracted |
| `tar-rootfs` | Tar archive of a root filesystem | `mkfs.ext4` + `tar -xf` into a fresh image |
| `raw` | Raw disk image | Root partition extracted directly |
| `squashfs` | SquashFS filesystem image | `unsquashfs` → `mkfs.ext4 -d` |

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
   falling back to `FALLBACK_FC_CI_VERSION` from `defaults.yaml`.
2. The host architecture is detected via `platform.machine()`, falling back to
   `kernel.defaults.arch`.
3. The S3 bucket defined by `urls.firecracker_ci_image.list_url_template` is queried
   to list all matching objects for that version and architecture.
4. The latest matching entry (lexicographic sort) is selected as the concrete download
   URL, rooted at `urls.firecracker_ci_kernel.s3_base`.

Any image entry whose `source` contains `{` triggers this path automatically — no
special `id` value or `format` is required.

### Current images

| ID | Name | Format | Filesystem | Size |
|----|------|--------|------------|------|
| `ubuntu-24.04` | Ubuntu 24.04 LTS (Noble) | `tar-rootfs` | ext4 | 2048 MiB |
| `ubuntu-22.04` | Ubuntu 22.04 LTS (Jammy) | `tar-rootfs` | ext4 | 2048 MiB |
| `archlinux` | Arch Linux | `qcow2` | btrfs | 4096 MiB |
| `debian-bookworm` | Debian 12 (Bookworm) | `qcow2` | ext4 | 2048 MiB |
| `ubuntu-fc` | Ubuntu (Firecracker CI) | `squashfs` | ext4 | 1024 MiB |

---

## kernels.yaml

**Path:** `src/mvmctl/assets/kernels.yaml`
**Consumed by:** `mvm kernel fetch --type official` (build pipeline)

Defines the default parameters for the official upstream kernel build workflow.
These values are used when the corresponding CLI flag is omitted.

### Structure

```yaml
kernel-official:
  version: <string>          # kernel version to fetch (e.g. "6.1.102")
  source: <url>              # tarball URL (can reference {version})
  sha256: <hex|null>         # expected digest of the tarball, or null
  sha256_url: <url|null>     # upstream checksum URL (informational)
  config_fragments:          # list of config overlay files to apply
    - <path>
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
```

### Field reference

| Field | Description |
|-------|-------------|
| `version` | Kernel version string passed to the build pipeline. Overridden by `--version` on the CLI. |
| `source` | Tarball download URL. At runtime this is superseded by `KERNEL_TARBALL_URL_TEMPLATE` from `defaults.yaml`. |
| `sha256` / `sha256_url` | Same semantics as [images.yaml SHA-256](#sha-256-semantics): `null` skips verification; a hex value is checked against the downloaded tarball. |
| `config_fragments` | Paths to additional kernel config files merged on top of the Firecracker baseline config. Relative paths are resolved from the project root. |
| `output_name` | Base filename for the compiled `vmlinux` binary in the kernels cache. |
| `build_dir` | Working directory for the kernel compilation. Cleaned up automatically unless `--keep-build-dir` is passed. |
| `parallel_jobs` | `make -j` value. `null` defers to `FALLBACK_KERNEL_BUILD_JOBS` (defaults to 1). |
| `enabled_configs` | List of kernel `CONFIG_*` options passed to `scripts/config --enable`. Applied before any user-supplied `--kernel-config` override. |
| `disabled_configs` | List of kernel `CONFIG_*` options passed to `scripts/config --disable`. |
| `set_val_configs` | List of `{option, value}` pairs passed to `scripts/config --set-val`. Each entry sets an integer-valued config option. |
| `required_settings` | List of `CONFIG_OPTION=y` strings that must be present in `.config` after the build. If any are missing the build prompts the user before continuing. |

---

## defaults.yaml

**Path:** `src/mvmctl/assets/defaults.yaml`
**Consumed by:** `constants.py` at import time — every value is read once, validated,
and exposed as a typed module-level constant.

This file is the **single authoritative source** for all built-in defaults. Hardcoded
values anywhere else in the codebase are a bug.

### Structure overview

```
firecracker:            Firecracker binary paths and version strings
vm_defaults:            Default vCPU count, RAM, SSH user, boot args, etc.
network.defaults:       Default bridge name, CIDR, gateway
vm.files:               Kernel and rootfs filenames inside a VM bundle
vm.logging:             Default log type/lines/follow behaviour
vm.snapshot:            Default snapshot resume behaviour
vm.limits:              Hard cap on simultaneous VMs
image.defaults:         Default convert_to, import format, supported extensions
image.remote:           Parallel download limits
host:                   Privileged binaries and system file paths
kernel.defaults:        Default kernel version and architecture
fallbacks:              Last-resort values when config lookup fails
urls:                   All external URL templates used by mvm
```

### `image.defaults`

| Key | Default | Description |
|-----|---------|-------------|
| `convert_to` | `ext4` | Filesystem type used when converting a downloaded image |
| `import_format` | `auto` | Source format assumed during `mvm image import` |
| `import_size_mib` | `2048` | Allocated size in MiB for images whose YAML entry omits `size_mib` |
| `supported_extensions` | `.ext4 .btrfs .img .raw` | File extensions scanned when looking up a locally cached image |
| `import_format_map` | (extension → format) | Auto-detection table used when `import_format: auto` is set |

### `urls`

All outbound URLs are centralised here. No URL strings appear anywhere else in the
source code.

| Key path | Description |
|----------|-------------|
| `urls.firecracker.github_releases_api` | GitHub API endpoint for Firecracker release metadata |
| `urls.firecracker.github_download_base` | Base URL for Firecracker release asset downloads |
| `urls.firecracker.github_raw_base` | Base URL for raw file access in the Firecracker repository |
| `urls.firecracker_ci_kernel.s3_base` | S3 base URL for all Firecracker CI artifacts (`https://s3.amazonaws.com/spec.ccfc.min`) |
| `urls.firecracker_ci_kernel.list_url_template` | S3 listing URL template for Firecracker CI **kernels**; placeholders: `{ci_version}`, `{arch}` |
| `urls.firecracker_ci_image.list_url_template` | S3 listing URL template for Firecracker CI **images**; placeholders: `{ci_version}`, `{arch}` |
| `urls.firecracker_kernel.config_url_template` | URL template for the recommended Firecracker kernel `.config`; placeholder: `{major_minor}` |
| `urls.kernel.tarball_template` | kernel.org tarball URL; placeholder: `{version}` |
| `urls.kernel.sha256_template` | kernel.org SHA-256 file URL; placeholder: `{version}` |

### `fallbacks`

Last-resort runtime values used when the user config file is absent or incomplete.
Unlike `vm_defaults`, these are never surfaced to the user directly.

| Key | Value | Used when |
|-----|-------|-----------|
| `fc_ci_version` | `1.15` | Active Firecracker CI version cannot be read from config |
| `firecracker_bin` | `firecracker` | Firecracker binary path is not configured |
| `kernel_build_jobs` | `1` | Parallel jobs not set in kernels.yaml |
| `max_parallel_downloads` | `4` | Worker limit for `fetch_images_parallel` |

---

## Configuration priority

Values are resolved in this order, from lowest to highest precedence:

```
1. defaults.yaml (fallback defaults — these are the floor)
2. Runtime state files:
   - `~/.config/mvmctl/config.json` for general config and assets paths
   - `~/.cache/mvmctl/metadata.json` for image/kernel/binary defaults (`is_default`)
3. MVM_* environment variables (e.g. MVM_CACHE_DIR, MVM_KERNEL)
4. CLI flags (e.g. --out, --force, --arch)
```

`images.yaml` and `kernels.yaml` define the available asset catalogue and are not part
of this priority chain. They cannot be overridden at runtime — to use a different image
source, add it to `images.yaml` and reinstall, or use `mvm image import` for local files.

---

## Adding a new image

To register a new rootfs image that can be fetched via `mvm image fetch`:

**1. Append an entry to `src/mvmctl/assets/images.yaml`:**

```yaml
- id: fedora-40                              # must be unique, no spaces
  name: "Fedora 40 (Cloud)"
  source: https://example.com/fedora-40-cloudimg.qcow2
  format: qcow2
  convert_to: ext4
  size_mib: 4096
  sha256: null                               # null = no checksum check
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
  format: squashfs
  convert_to: ext4
  size_mib: 1024
  sha256: null
  sha256_url: null
```

Any `source` value containing `{` is automatically treated as a template and passed to
the dynamic resolver at fetch time. Make sure `urls.firecracker_ci_image.list_url_template`
in `defaults.yaml` points to the correct S3 listing endpoint for the chosen prefix.

**3. Verify the new entry is visible:**

```bash
uv run mvm image ls --remote
```

The new ID should appear in the table. Fetch it to confirm end-to-end:

```bash
uv run mvm image fetch <your-new-id>
```

---

## Constants reference

`constants.py` reads `defaults.yaml` at import time and exposes every value as a typed
`Final` constant. The table below lists the constants relevant to asset management.

| Constant | Source key | Description |
|----------|------------|-------------|
| `DEFAULT_IMAGE_CONVERT_TO` | `image.defaults.convert_to` | Default filesystem type for image conversion |
| `DEFAULT_IMAGE_IMPORT_FORMAT` | `image.defaults.import_format` | Default source format for `mvm image import` |
| `DEFAULT_IMAGE_IMPORT_SIZE_MIB` | `image.defaults.import_size_mib` | Fallback image size when `size_mib` is absent in YAML |
| `SUPPORTED_IMAGE_EXTENSIONS` | `image.defaults.supported_extensions` | File extensions scanned for cached images |
| `IMAGE_IMPORT_FORMAT_MAP` | `image.defaults.import_format_map` | Extension → format auto-detection table |
| `DEFAULT_REMOTE_VERSION_LIMIT` | `image.remote.version_limit` | Max remote versions shown by `mvm bin ls --remote` |
| `FALLBACK_MAX_PARALLEL_DOWNLOADS` | `fallbacks.max_parallel_downloads` | Default worker count for parallel image fetches |
| `DEFAULT_KERNEL_VERSION` | `kernel.defaults.version` | Default version for `mvm kernel fetch --type official` |
| `DEFAULT_FC_KERNEL_ARCH` | `kernel.defaults.arch` | Default architecture for kernel operations |
| `FALLBACK_KERNEL_BUILD_JOBS` | `fallbacks.kernel_build_jobs` | Default `make -j` value when not specified |
| `KERNEL_TARBALL_URL_TEMPLATE` | `urls.kernel.tarball_template` | kernel.org tarball URL; fill `{version}` |
| `KERNEL_SHA256_URL_TEMPLATE` | `urls.kernel.sha256_template` | kernel.org SHA-256 URL; fill `{version}` |

> **Note:** The kernel config lists (`enabled_configs`, `disabled_configs`, `set_val_configs`,
> `required_settings`) are defined per-kernel in `kernels.yaml`, not as module-level constants.
> Load them at runtime via `core.kernel.load_kernel_spec("kernel-official")`, which returns a
> fully typed `KernelSpec` dataclass.
| `FIRECRACKER_CI_KERNEL_S3_BASE` | `urls.firecracker_ci_kernel.s3_base` | S3 base URL for all Firecracker CI artifacts |
| `FIRECRACKER_CI_KERNEL_LIST_URL` | `urls.firecracker_ci_kernel.list_url_template` | S3 listing template for CI kernels; fill `{ci_version}`, `{arch}` |
| `FIRECRACKER_CI_IMAGE_LIST_URL` | `urls.firecracker_ci_image.list_url_template` | S3 listing template for CI images; fill `{ci_version}`, `{arch}` |
| `FALLBACK_FC_CI_VERSION` | `fallbacks.fc_ci_version` | CI version used when config lookup fails |
| `DEFAULT_FIRECRACKER_VERSION` | `firecracker.versions.full` | Default Firecracker binary version |
| `DEFAULT_FIRECRACKER_CI_VERSION` | `firecracker.versions.ci` | Default Firecracker CI version for kernel/image downloads |

---

*See also: [custom-kernel.md](custom-kernel.md) for the kernel build workflow,
[API.md](API.md) for the Python API reference.*
