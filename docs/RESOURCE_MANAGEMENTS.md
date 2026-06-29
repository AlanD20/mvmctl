# Assets Configuration Reference

This document describes the three bundled YAML/template files that drive asset management in `mvm`, how each field is interpreted at runtime, and how to extend them.

All three files live under `internal/assets/` and are embedded into the Go binary via `//go:embed`. They are read-only at runtime — user overrides are resolved from the SQLite database (`~/.cache/mvmctl/mvmdb.db`) and configuration settings, not by editing these files directly.

The asset YAML files are:
- `images.yaml` — catalogue of image types with version resolvers
- `kernels.yaml` — kernel build/download specifications
- `cloud-init.template.yaml` — template for cloud-init user-data

## Table of Contents

- [images.yaml](#imagesyaml)
  - [Structure](#structure)
  - [Resolver strategies](#resolver-strategies)
  - [Field reference](#field-reference)
  - [Options reference](#options-reference)
  - [SHA-256 semantics](#sha-256-semantics)
  - [Format types](#format-types)
  - [Template variables](#template-variables)
  - [Current image types](#current-image-types)
  - [Using image types on the CLI](#using-image-types-on-the-cli)
- [kernels.yaml](#kernelsyaml)
  - [Structure](#structure-1)
  - [Field reference](#field-reference-1)
- [Runtime defaults (constants.go)](#runtime-defaults-constantsgo)
  - [Structure overview](#structure-overview)
  - [defaults.kernel](#defaultskernel)
  - [defaults.image](#defaultsimage)
  - [Related constants](#related-constants)
- [Adding a new image type](#adding-a-new-image-type)

---

## images.yaml

**Path:** `internal/assets/images.yaml`
**Consumed by:** `mvm image pull`, `mvm image ls --remote`

Defines the catalogue of image types available via `mvm image pull --type <type>`. The file defines image type templates with version resolvers. A specific version is selected at fetch time based on the upstream directory listing or explicit `--version` flag.

### Structure

The file uses a single top-level key `image_types` containing an ordered list of type definitions:

```yaml
image_types:
  - type: <string>
    name: <string>
    resolver: <string|null>
    version_name_template: <string>
    versions_url: <url|null>
    download_url: <url>
    sha256_url: <url|null>
    format: <string>
    options:
      skip_patterns: [...]
      codename_mapping: {...}
      arch_mapping: {...}
      version_prefix: <string>
      file_discovery:
        enabled: <bool>
        pattern: <string>
        suffix: <string>
        sha256_suffix: <string>
```

### Resolver strategies

| `resolver` | Description | Example types |
|------------|-------------|---------------|
| `http-dir` | Fetches an HTML directory listing from `versions_url`, parses version directories, and selects the latest (or requested) version. Supports codename-to-version mapping and architecture name mapping. | `ubuntu`, `debian`, `alpine` |
| `firecracker-s3` | Uses S3 XML listing (`list_url_template`) to discover versions for a given CI version and architecture. | `firecracker` |
| `null` / `""` | Single-source type with a fixed download URL. No version resolution — always uses `"latest"` as the version. | `archlinux` |

### Field reference

| Field | Required | Description |
|-------|----------|-------------|
| `type` | **Yes** | Short identifier used on the CLI (`mvm image pull --type <type>`). Must be unique. |
| `name` | **Yes** | Human-readable label shown in `mvm image ls --remote`. |
| `resolver` | No | Resolution strategy: `"http-dir"`, `"firecracker-s3"`, or `null` for single-source. |
| `version_name_template` | No | Go `text/template` for the display name. Variables: `{version}`, `{codename}`, `{type}`, `{ci_version}`. |
| `versions_url` | Yes (http-dir) | URL to fetch the directory listing from. |
| `download_url` | **Yes** | URL template for the actual image file. Supports `{version}`, `{codename}`, `{arch}`, `{ci_version}`. |
| `sha256_url` | No | URL template for the SHA-256 checksum file. May be `null` when checksums are not available. |
| `format` | **Yes** | Format of the downloaded file. See Format types below. |
| `list_url_template` | No | S3 XML listing URL template for `firecracker-s3` resolver. Placeholders: `{ci_version}`, `{arch}`, `{version}`. |
| `options` | No | Resolver-specific configuration. |

### Options reference

| Option | Used by | Description |
|--------|---------|-------------|
| `skip_patterns` | http-dir | List of substrings that cause a directory entry to be ignored (e.g. `"Parent Directory"`, `"edge"`). |
| `codename_mapping` | http-dir | Maps upstream codenames to mvm version strings. The reverse mapping (version → codename) is used to construct download URLs. |
| `arch_mapping` | http-dir, single-source | Maps mvm architecture names (`x86_64`, `aarch64`) to upstream architecture names (`amd64`, `arm64`). |
| `version_prefix` | http-dir | String prepended to the version when constructing directory URLs (e.g. Alpine uses `"v"` → `v3.21`). |
| `file_discovery.enabled` | http-dir | When `true`, the resolver fetches the directory listing at `download_url` and searches for filenames matching `pattern` + `suffix`. |
| `file_discovery.pattern` | file_discovery | Filename prefix pattern to match when scanning directory listings. |
| `file_discovery.suffix` | file_discovery | Filename suffix to match (e.g. `"-bios-cloudinit-r"`). |
| `file_discovery.sha256_suffix` | file_discovery | Checksum file suffix for file-discovery types (e.g. `".sha512"` for Alpine). |
| `convert_to` | single-source | Target filesystem format for single-source types that need conversion (e.g. `ext4`). |

### SHA-256 semantics

SHA-256 checksum resolution differs per image type:

- **Ubuntu / Debian / Arch**: `sha256_url` template resolves to an upstream SHA256SUMS file. The fetch logic downloads the checksum file and extracts the matching digest for the downloaded filename.
- **Alpine**: `sha256_url` is `null`; the `file_discovery.sha256_suffix` (`.sha512`) is appended to the discovered filename. Alpine provides SHA-512 rather than SHA-256 checksums.
- **Firecracker CI**: `sha256_url` is `null`. No upstream checksum is published for Firecracker CI S3 assets. The download proceeds without checksum verification.

### Format types

| `format` value | Source file | Conversion |
|----------------|-------------|------------|
| `qcow2` | QEMU copy-on-write v2 image | `qemu-img convert` → raw → root partition extracted |
| `tar-rootfs` | Tar archive of a root filesystem | `mkfs.ext4 -d` + `tar -xf` into a fresh image |
| `raw` | Raw disk image | Root partition extracted directly |
| `squashfs` | SquashFS filesystem image | `unsquashfs` → `mkfs.ext4 -d` |
| `vhd` | Microsoft VHD image | `qemu-img convert` (as vpc) → raw → root partition extracted |
| `vhdx` | Microsoft VHDX image | `qemu-img convert` (as vhdx) → raw → root partition extracted |

### Template variables

URL templates use `{placeholder}` syntax resolved at fetch time:

| Variable | Source | Description |
|----------|--------|-------------|
| `{version}` | CLI `--version` or version resolver | OS version (e.g. `24.04`, `12`) |
| `{codename}` | Reverse lookup from `codename_mapping` | Upstream codename (e.g. `noble`, `bookworm`) |
| `{arch}` | Detected host architecture | Target architecture (mapped via `arch_mapping`) |
| `{ci_version}` | Default Firecracker CI version | Firecracker CI version (e.g. `v1.15`) |

**Resolution flow:**

1. If `resolver` is `null` (single-source): version is always `"latest"`, templates render directly.
2. If `resolver` is `http-dir` with explicit `--version`: builds URLs directly from templates (no HTTP fetch) if the type has no `file_discovery`. Otherwise fetches the directory listing.
3. If `resolver` is `http-dir` without explicit version or with `file_discovery`: fetches the `versions_url` directory listing via `HttpDirVersionResolver`, picks the latest version (or matching version), and renders templates.
4. If `resolver` is `firecracker-s3`: uses S3 XML listing via `list_url_template` to discover available kernel versions for the `ci_version` and `arch`. The latest version (by semver) is selected.

### Current image types

The YAML at `internal/assets/images.yaml` currently defines 6 image types:

| Type | Name | Format | Resolver | Versions |
|------|------|--------|----------|----------|
| `ubuntu` | Ubuntu LTS | `tar-rootfs` | http-dir | 20.04 (focal), 22.04 (jammy), 24.04 (noble), 26.04 (resolute) |
| `ubuntu-minimal` | Ubuntu Minimal | `tar-rootfs` | http-dir | 20.04 (focal), 22.04 (jammy), 24.04 (noble), 26.04 (resolute) |
| `debian` | Debian | `qcow2` | http-dir | 11 (bullseye), 12 (bookworm), 13 (trixie) |
| `alpine` | Alpine Linux | `vhd` | http-dir | Dynamic — discovers version directories upstream |
| `archlinux` | Arch Linux | `qcow2` | null (single-source) | `latest` |
| `firecracker` | Firecracker CI Ubuntu | `squashfs` | firecracker-s3 | Dynamic — resolves via S3 listing |

> Alpine is the only type using `file_discovery` — it discovers the exact filename from an Alpine cloud directory listing rather than using a fixed download URL. It also uses `.sha512` checksums (Alpine upstream provides SHA-512).
>
> Firecracker CI has `sha256_url: null` (no upstream checksum file). The image is fetched from a Firecracker CI S3 bucket that does not publish sidecar checksums.

### Using image types on the CLI

```bash
# List all available remote image types
mvm image ls --remote

# Pull the latest Ubuntu 24.04
mvm image pull --type ubuntu

# Pull a specific version
mvm image pull --type ubuntu --version 24.04

# Shorthand type:version syntax
mvm image pull ubuntu:24.04

# Pull a Debian image
mvm image pull --type debian --version 12
```

---

## kernels.yaml

**Path:** `internal/assets/kernels.yaml`
**Consumed by:** `mvm kernel pull` (build pipeline or direct download)

Defines the default parameters for the official upstream kernel build workflow and the Firecracker CI kernel download workflow. Supports dynamic version resolution via resolver strategies.

### Structure

```yaml
kernel-official:
  type: official
  version: <string>
  resolver: http-dir
  versions_url: <url>
  source: <url>
  sha256: <hex|null>
  sha256_url: <url|null>
  config_url_template: <url>
  config_fragments:
    - <path_or_url>
  output_name: <string>
  build_dir: <path>
  parallel_jobs: <int|null>
  default_configs:
    CONFIG_EXT4_FS: y
    CONFIG_BLK_DEV_ZONED: n
  features:
    <feature_name>:
      desc: <string>
      enforce:
        CONFIG_OPTION: y
  options:
    version_discoveries:
      - "v6.x"
    file_pattern: <string>
    file_suffix: <string>

kernel-firecracker:
  type: firecracker
  version: <string>
  resolver: firecracker-s3
  source: <url>
  list_url_template: <url>
  config_url_template: <url>
  output_name: <string>
  build_dir: <path>
  sha256: <hex|null>
  sha256_url: <url|null>
  config_fragments: []
  parallel_jobs: null
  default_configs: {}
  options:
    s3_version_pattern: <regex>
```

### Field reference

| Field | Description |
|-------|-------------|
| `type` | Kernel category: `official` (built from source) or `firecracker` (pre-built CI kernel). |
| `version` | Kernel version string. Overridden by `--version` on the CLI. |
| `resolver` | Version resolution strategy: `http-dir` for kernel.org directory listings, `firecracker-s3` for Firecracker CI S3 bucket. |
| `versions_url` | URL template for listing available kernel versions (`http-dir` resolver only). |
| `source` | Tarball download URL or S3 base URL. |
| `list_url_template` | S3 listing URL template for Firecracker CI kernels. Placeholders: `{ci_version}`, `{arch}`, `{version}`. |
| `config_url_template` | URL template to download the base `.config` file. |
| `sha256` / `sha256_url` | Same semantics as images.yaml SHA-256. |
| `config_fragments` | Paths or URLs to additional kernel config files merged on top of the base config. |
| `output_name` | Base filename for the compiled or downloaded `vmlinux` binary in the kernels cache. |
| `build_dir` | Working directory for kernel compilation. Cleaned up automatically unless `--keep-build-dir` is passed. |
| `parallel_jobs` | `make -j` value. `null` defers to `defaults.kernel.build_jobs` (default: `nil`, meaning all available cores). |
| `default_configs` | Flat map of `CONFIG_OPTION: value` entries. `y` enables the option, `n` disables it, string values set the option to a specific value (e.g. `"4"`). Passed to `scripts/config` during build. |
| `features` | Named feature groups of kernel config options. Each feature has `desc` and `enforce` (a map of `CONFIG_OPTION: y` values). Activatable via `--features` on `mvm kernel pull`. |
| `options.version_discoveries` | List of subdirectory patterns to scan on kernel.org (e.g. `v6.x`, `v7.x`) for version discovery. |
| `options.file_pattern` | Filename prefix pattern for matching tarball entries (e.g. `linux-`). |
| `options.file_suffix` | Filename suffix for matching tarball entries (e.g. `.tar.xz`). |
| `options.s3_version_pattern` | Regex pattern for extracting version strings from S3 listing keys (`firecracker-s3` resolver only). |

---

## Runtime defaults (constants.go)

**Location:** `internal/infra/constants.go` — `OverridableDefaults` map

This map is the single authoritative source for all built-in defaults. Hardcoded values anywhere else in the codebase are a bug.

### Structure overview

| Category path | Description |
|---------------|-------------|
| `defaults.vm` | Default vCPU count, RAM, SSH user, boot args, LSM flags, PCI, nested virt, logging, console, vsock port |
| `defaults.network` | Default bridge name, CIDR, NAT enabled |
| `defaults.image` | Import format, remote listing limit and cache TTL |
| `defaults.kernel` | Default kernel version, build jobs, remote listing limit and cache TTL |
| `defaults.firecracker` | Log filenames, socket filenames, log level, PID filenames |
| `defaults.cloudinit` | ISO name, nocloud-net port range, max retries, kill-after duration |
| `defaults.binary` | Remote version limit |
| `defaults.volume` | Cache type (default: `Unsafe`) |
| `settings` | General settings: guestfs_enabled, firewall_backend |
| `settings.firewall` | iptables_xtcomment flag |
| `settings.vm` | log_lines, log_follow, max_vms, ssh_timeout_sec |
| `cli` | Listing style (default: `short`) |

### `defaults.kernel`

| Key | Default | Description |
|-----|---------|-------------|
| `version` | `6.19.9` | Default kernel version for `mvm kernel pull --type official` |
| `build_jobs` | `nil` | Parallel compilation jobs (`nil` = all available cores) |
| `remote_list_limit` | `5` | Max entries for remote version listing |
| `remote_list_cache_ttl` | `14400` | Cache TTL for remote version listing (seconds, 4 hours) |

### `defaults.image`

| Key | Default | Description |
|-----|---------|-------------|
| `import_format` | `auto` | Default import format when no `--format` specified |
| `remote_list_limit` | `5` | Max remote versions to list per type |
| `remote_list_cache_ttl` | `3600` | Cache TTL for remote version listing (seconds, 1 hour) |

### Related constants

| Constant | Description |
|----------|-------------|
| `DefaultFirecrackerCIVersion` (`"v1.15"`) | CI version used when config lookup fails |
| `SupportedImageExtensions` | File extensions scanned for cached images: `.ext4`, `.btrfs`, `.img`, `.raw`, `.ext4.zst`, `.btrfs.zst` |
| `ImageImportFormatMap` | Extension → format auto-detection table (`.qcow2` → `qcow2`, `.tar.gz` → `tar-rootfs`, etc.) |
| `HTTPTimeoutKernelDownloadS` (`600`) | Timeout for kernel tarball download (seconds) |
| `HTTPTimeoutKernelConfigS` (`60`) | Timeout for kernel config download (seconds) |
| `HTTPTimeoutSha256FetchS` (`30`) | Timeout for SHA-256 checksum fetch (seconds) |
| `FirecrackerGithubReleasesAPIURL` | GitHub API endpoint for Firecracker releases |
| `FirecrackerGithubDownloadURL` | Base URL for Firecracker release assets |

The kernel config map (`default_configs`) and feature groups (`features`) are defined per-kernel in `kernels.yaml`, not as package-level constants.

---

`images.yaml` and `kernels.yaml` define the available asset catalogue and are not part of the configuration priority chain. They cannot be overridden at runtime — to use a different image source, add it to `images.yaml` and reinstall, or use `mvm image import` for local files.

---

## Adding a new image type

To register a new image type that can be fetched via `mvm image pull --type <type>`:

**1. Add an entry to the `image_types` list in `internal/assets/images.yaml`:**

For a type with a standard version resolver (http-dir):
```yaml
- type: fedora
  name: "Fedora Cloud"
  resolver: http-dir
  version_name_template: "Fedora {version} (Cloud)"
  versions_url: "https://download.fedoraproject.org/pub/fedora/linux/releases/"
  download_url: "https://download.fedoraproject.org/pub/fedora/linux/releases/{version}/Cloud/{arch}/images/Fedora-Cloud-Base-{version}-{arch}.qcow2"
  sha256_url: "https://download.fedoraproject.org/pub/fedora/linux/releases/{version}/Cloud/{arch}/images/Fedora-Cloud-{version}-{arch}-CHECKSUM"
  format: qcow2
  options:
    skip_patterns:
      - "test"
      - "README"
    arch_mapping:
      x86_64: "x86_64"
      aarch64: "aarch64"
```

For a single-source type (no version resolution):
```yaml
- type: my-fixed-image
  name: "My Fixed Image"
  resolver: null
  version_name_template: "My Image"
  download_url: "https://example.com/my-image.qcow2"
  sha256_url: "https://example.com/my-image.qcow2.SHA256"
  format: qcow2
  options: {}
```

**2. Template variables supported in URL fields:**

| Variable | Description |
|----------|-------------|
| `{version}` | OS version string (e.g. `40`), or `latest` for single-source types |
| `{codename}` | Upstream codename (for http-dir types with `codename_mapping`) |
| `{arch}` | Target architecture (mapped through `arch_mapping` if present) |
| `{ci_version}` | Firecracker CI version (firecracker-s3 types only) |

**3. Verify the new entry is visible:**

```bash
mvm image ls --remote
```

The new type should appear in the table. Fetch it to confirm end-to-end:

```bash
mvm image pull --type fedora --version 40
```

> The `id` shown in `mvm image ls` is a SHA-256 hash generated from the type, download URL, and timestamp. It is NOT derived from `{type}-{version}`. The ID is computed at pull time.
