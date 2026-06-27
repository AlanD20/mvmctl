# Assets Configuration Reference

This document describes the three bundled YAML/template files that drive asset management in
`mvm`, how each field is interpreted at runtime, and how to extend them.

All three files live under `internal/assets/` and are embedded into the Go binary via
`//go:embed`. They are read-only at runtime — user overrides are resolved from the SQLite
database (`~/.cache/mvmctl/mvmdb.db`) and configuration settings, not by editing
these files directly.

The asset YAML files are:
- `images.yaml` — catalogue of image types with version resolvers
- `kernels.yaml` — kernel build/download specifications
- `cloud-init.template.yaml` — template for cloud-init user-data

---

## Table of Contents

- [images.yaml](#imagesyaml)
- [kernels.yaml](#kernelsyaml)
- [Runtime defaults (constants.go)](#runtime-defaults-constantsgo)
- [Configuration priority](#configuration-priority)
- [Adding a new image](#adding-a-new-image)
- [Constants reference](#constants-reference)

---

## images.yaml

**Path:** `internal/assets/images.yaml`
**Consumed by:** `mvm image pull`, `mvm image ls --remote`, `image.GetSpecsFor()`, `image.LoadImageTypesConfig()`

Defines the catalogue of image types available via `mvm image pull --type <type>`. Unlike a flat list of versioned images, this file defines **image type templates** with version resolvers. A specific version is selected at fetch time based on the upstream directory listing or explicit `--version` flag.

### Structure

The file uses a single top-level key `image_types` containing an ordered list of type definitions:

```yaml
image_types:
  - type: <string>              # OS family identifier (ubuntu, debian, alpine, etc.)
    name: <string>              # human-readable display name
    resolver: <string|null>     # resolution strategy: "http-dir", "firecracker-s3", or null
    version_name_template: <str># Go text/template for display name
    versions_url: <url|null>    # URL to fetch directory listing from (http-dir resolver)
    download_url: <url>         # download URL template; placeholders: {version}, {codename}, {arch}
    sha256_url: <url|null>      # upstream checksum URL template, or null
    format: <string>            # source file format (see Format types)
    list_url_template: <url|null> # S3 listing URL template (firecracker-s3 resolver only)
    options:                    # resolver-specific configuration
      skip_patterns: [...]      # strings to filter out from directory listings
      codename_mapping: {...}   # codename → version mapping (e.g. noble → 24.04)
      arch_mapping: {...}       # mvm arch → upstream arch mapping (e.g. x86_64 → amd64)
      version_prefix: <string>  # prefix to add to version for URL construction (e.g. "v")
      file_discovery:           # configuration for dynamic filename resolution
        enabled: <bool>
        pattern: <string>       # glob-style filename pattern to match
        suffix: <string>        # expected filename suffix
        sha256_suffix: <string> # checksum file suffix (e.g. ".sha512")
```

### Resolver strategies

| `resolver` | Description | Example |
|------------|-------------|---------|
| `http-dir` | Fetches an HTML directory listing from `versions_url`, parses version directories, and selects the latest (or requested) version. Supports codename → version and arch mappings. | `ubuntu`, `debian`, `alpine` |
| `firecracker-s3` | Uses S3 XML listing (`list_url_template`) to discover versions for a given CI version and architecture. | `firecracker` |
| `null` / `""` | Single-source type with a fixed download URL. No version resolution — always uses `"latest"` as the version. | `archlinux` |

### Field reference

| Field | Required | Description |
|-------|----------|-------------|
| `type` | ✅ | Short identifier used on the CLI (`mvm image pull --type <type>`). Must be unique. |
| `name` | ✅ | Human-readable label shown in `mvm image ls`. |
| `resolver` | — | Resolution strategy. `"http-dir"` fetches directory listings, `"firecracker-s3"` uses S3 XML listings, `null` means a fixed single-source URL. |
| `version_name_template` | — | Go `text/template` for the display name (variables: `{version}`, `{codename}`, `{type}`, `{ci_version}`). |
| `versions_url` | ✅ (http-dir) | URL to fetch the directory listing from. |
| `download_url` | ✅ | URL template for the actual image file. Supports `{version}`, `{codename}`, `{arch}`, `{ci_version}`, and type-specific variables. |
| `sha256_url` | — | URL template for the SHA-256 checksum file. May be `null` when checksums are not available or handled differently. |
| `format` | ✅ | Format of the downloaded file. See [Format types](#format-types). |
| `list_url_template` | — | S3 XML listing URL template for `firecracker-s3` resolver. Placeholders: `{ci_version}`, `{arch}`, `{version}`. |
| `options` | — | Resolver-specific configuration (see [Options reference](#options-reference)). |

### Options reference

| Option | Used by | Description |
|--------|---------|-------------|
| `skip_patterns` | http-dir | List of substrings that cause a directory entry to be ignored (e.g. `"Parent Directory"`, `"edge"`). |
| `codename_mapping` | http-dir | Maps upstream codenames to mvm version strings. The reverse mapping (version → codename) is used to construct download URLs. |
| `arch_mapping` | http-dir, single-source | Maps mvm architecture names (e.g. `x86_64`, `aarch64`) to upstream architecture names (e.g. `amd64`, `arm64`). |
| `version_prefix` | http-dir | String prepended to the version when constructing directory URLs (e.g. Alpine uses `"v"` → `v3.21`). |
| `file_discovery.enabled` | http-dir | When `true`, the resolver does not use a fixed download URL. Instead it fetches the directory listing at `download_url` and searches for filenames matching `pattern` + `suffix`. |
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

### Template sources & version resolution

URL templates use `{placeholder}` syntax resolved at fetch time. Variables include:

| Variable | Source | Description |
|----------|--------|-------------|
| `{version}` | CLI flag `--version` or version resolver | OS version (e.g. `24.04`, `12`) |
| `{codename}` | Reverse lookup from `codename_mapping` | Upstream codename (e.g. `noble`, `bookworm`) |
| `{arch}` | Detected host architecture | Target architecture (mapped via `arch_mapping`) |
| `{ci_version}` | Default firecracker CI version | Firecracker CI version (e.g. `v1.15`) |

**Resolution flow** (`ConstructSpecFromTypeConfig`):

1. If `resolver` is `null` (single-source): version is always `"latest"`, templates render directly.
2. If `resolver` is `http-dir` with explicit `--version`: builds URLs directly from templates (no HTTP fetch) if the type has no `file_discovery`. Otherwise fetches the directory listing.
3. If `resolver` is `http-dir` without explicit version or with `file_discovery`: fetches the `versions_url` directory listing via `HttpDirVersionResolver`, picks the latest version (or matching version), and renders templates.
4. If `resolver` is `firecracker-s3`: uses S3 XML listing via `list_url_template` to discover available kernel versions for the `ci_version` and `arch`. The latest version (by semver) is selected.

### Current image types

The actual YAML (`internal/assets/images.yaml`) currently defines 6 image types:

| Type | Name | Format | Resolver | Versions |
|------|------|--------|----------|----------|
| `ubuntu` | Ubuntu LTS | `tar-rootfs` | http-dir | 20.04 (focal), 22.04 (jammy), 24.04 (noble), 26.04 (resolute) |
| `ubuntu-minimal` | Ubuntu Minimal | `tar-rootfs` | http-dir | 20.04 (focal), 22.04 (jammy), 24.04 (noble), 26.04 (resolute) |
| `debian` | Debian | `qcow2` | http-dir | 11 (bullseye), 12 (bookworm), 13 (trixie) |
| `alpine` | Alpine Linux | `vhd` | http-dir | Dynamic — discovers version directories upstream |
| `archlinux` | Arch Linux | `qcow2` | (null — single source) | `latest` |
| `firecracker` | Firecracker CI Ubuntu | `squashfs` | firecracker-s3 | Dynamic — resolves via S3 listing |

> **Note:** `alpine` is the only type using `file_discovery` — it discovers the exact filename from an Alpine cloud directory listing rather than using a fixed download URL. It also uses `.sha512` checksums (Alpine upstream provides SHA-512).
>
> **Note:** `firecracker` has `sha256_url: null` (no upstream checksum file). The image is fetched from a Firecracker CI S3 bucket that does not publish sidecar checksums.

### Using image types on the CLI

```bash
# List all available remote image types
mvm image ls --remote

# Pull the latest Ubuntu 24.04
mvm image pull --type ubuntu

# Pull a specific version
mvm image pull --type ubuntu --version 24.04

# Shorthand type:version syntax (equivalent to --type ubuntu --version 24.04)
mvm image pull ubuntu:24.04

# Pull a Debian image
mvm image pull --type debian --version 12
```

---

## kernels.yaml

**Path:** `internal/assets/kernels.yaml`
**Consumed by:** `mvm kernel pull` (build pipeline or direct download)

Defines the default parameters for the official upstream kernel build workflow and the Firecracker CI kernel download workflow. Supports dynamic version resolution via resolver strategies (like images.yaml).

### Structure

```yaml
kernel-official:
  type: official               # kernel type (official or firecracker)
  version: <string>          # kernel version to fetch (e.g. "6.19.9")
  resolver: http-dir          # version resolution strategy
  versions_url: <url>         # URL for listing available kernel versions
  source: <url>              # tarball URL (can reference {version} and {series})
  sha256: <hex|null>         # expected digest of the tarball, or null
  sha256_url: <url|null>     # upstream checksum URL (uses {series} template)
  config_url_template: <url> # URL to fetch the base config from
  config_fragments:          # list of config overlay files or URLs to apply
    - <path_or_url>
  output_name: <string>      # filename for the built vmlinux
  build_dir: <path>          # temporary directory used during compilation
  parallel_jobs: <int|null>  # build parallelism; null = use FALLBACK_KERNEL_BUILD_JOBS
  default_configs:           # flat map of CONFIG_OPTION: value (y=enable, n=disable, string=set-val)
    CONFIG_EXT4_FS: y
    CONFIG_BLK_DEV_ZONED: n
    CONFIG_SERIAL_8250_NR_UARTS: "4"
  features:                  # named feature groups activatable via --features
    <feature_name>:
      desc: <string>
      enforce:
        CONFIG_OPTION: y
  options:                   # resolver-specific configuration
    version_discoveries:     # subdirectory patterns to scan on kernel.org
      - "v6.x"
      - "v7.x"
    file_pattern: <string>  # filename prefix pattern for matching tarball entries
    file_suffix: <string>   # filename suffix for matching tarball entries

kernel-firecracker:
  type: firecracker
  version: <string>
  resolver: firecracker-s3    # version resolution strategy
  source: <url>
  list_url_template: <url>   # S3 listing URL template for dynamically resolving the latest binary
  config_url_template: <url> # Optional base config template
  output_name: <string>
  build_dir: <path>
  sha256: <hex|null>
  sha256_url: <url|null>
  config_fragments: []
  parallel_jobs: null
  default_configs: {}
  options:                   # resolver-specific configuration
    s3_version_pattern: <regex> # regex for extracting version strings from S3 listing keys
```

### Field reference

| Field | Description |
|-------|-------------|
| `type` | Whether this entry represents an `official` (built from source) or `firecracker` (pre-built) kernel. |
| `version` | Kernel version string. Overridden by `--version` on the CLI. |
| `resolver` | Version resolution strategy: `http-dir` for kernel.org directory listings, `firecracker-s3` for Firecracker CI S3 bucket. When set, the kernel supports dynamic version listing and `type:version` shorthand. |
| `versions_url` | URL template for listing available kernel versions (`http-dir` resolver only). |
| `source` | Tarball download URL or S3 base URL. |
| `list_url_template` | S3 listing URL template for Firecracker CI kernels; placeholders: `{ci_version}`, `{arch}`, `{version}`. |
| `config_url_template` | URL template to download the base `.config` file. |
| `sha256` / `sha256_url` | Same semantics as [images.yaml SHA-256](#sha-256-semantics). |
| `config_fragments` | Paths or URLs to additional kernel config files merged on top of the base config. |
| `output_name` | Base filename for the compiled or downloaded `vmlinux` binary in the kernels cache. |
| `build_dir` | Working directory for the kernel compilation. Cleaned up automatically unless `--keep-build-dir` is passed. |
| `parallel_jobs` | `make -j` value. `null` defers to `defaults.kernel.build_jobs` (default: `nil`, which means all available cores). |
| `default_configs` | Flat map of `CONFIG_OPTION: value` entries. Values of `y` enable the option, `n` disables it, and string values set it to a specific value (e.g. `"4"`). Passed to `scripts/config --enable/--disable/--set-val` during build. |
| `features` | Named feature groups of kernel config options, loaded from the `features` key in `kernels.yaml`. Each feature has `desc` and `enforce` (a map of `CONFIG_OPTION: y` values to enforce). Activatable via `--features` on `mvm kernel pull`. |
| `options.version_discoveries` | List of subdirectory patterns to scan on kernel.org (e.g. `v6.x`, `v7.x`) for version discovery. |
| `options.file_pattern` | Filename prefix pattern for matching tarball entries (e.g. `linux-`). |
| `options.file_suffix` | Filename suffix for matching tarball entries (e.g. `.tar.xz`). |
| `options.s3_version_pattern` | Regex pattern for extracting version strings from S3 listing keys (`firecracker-s3` resolver only). |

---

## Runtime defaults (constants.go)

**Location:** `internal/infra/constants.go` — `OverridableDefaults` map

This map is the **single authoritative source** for all built-in defaults. Hardcoded
values anywhere else in the codebase are a bug.

### Structure overview

```
defaults.vm:            Default vCPU count, RAM, SSH user, boot args, LSM flags, etc.
defaults.network:       Default bridge name, CIDR, NAT enabled
defaults.image:         Import format, remote listing limit and cache TTL
defaults.kernel:        Default kernel version, architecture, build_jobs
defaults.firecracker:   Log filenames, socket filenames, log level
defaults.cloudinit:     ISO name, nocloud-net port range
defaults.binary:        Remote version limit for bin ls
settings:               General settings (guestfs_enabled, firewall_backend)
settings.firewall:      iptables_xtcomment flag
settings.vm:            Log lines, log follow, max_vms, ssh_timeout_sec
defaults.volume:        Volume cache type
```

Additional constants are defined as package-level variables for HTTP timeouts, URLs, file
permissions, and other fixed values.

### `defaults.kernel`

| Key | Default | Description |
|-----|---------|-------------|
| `version` | `6.19.9` | Default kernel version |
| `build_jobs` | `nil` | Parallel compilation jobs (`nil` = all available cores) |
| `remote_list_limit` | `5` | Max entries for remote version listing |
| `remote_list_cache_ttl` | `14400` | Cache TTL for remote version listing (seconds) |

### Related constants

| Constant | Source | Description |
|----------|--------|-------------|
| `DEFAULT_FIRECRACKER_CI_VERSION` | `constants.go` (standalone constant) | CI version used when config lookup fails — resolves to `v1.15` at runtime |

---

`images.yaml` and `kernels.yaml` define the available asset catalogue and are not part
of the [configuration priority chain](REFERENCES.md#configuration). They cannot be overridden
at runtime — to use a different image source, add it to `images.yaml` and reinstall,
or use `mvm image import` for local files.

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
| `{codename}` | Upstream codename (for http-dir types with codename_mapping) |
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

> **Note:** The `id` shown in `mvm image ls` is a SHA-256 hash generated from the type, download URL, and timestamp. It is NOT derived from `{type}-{version}`. You do not specify the ID in the YAML — it is computed at pull time via `crypto.ImageID()`.

---

## Constants reference

`internal/infra/constants.go` stores runtime defaults in the `OverridableDefaults` map and
exposes both package-level constants and computed values. The table below lists
the constants relevant to asset management.

| Access pattern | Config path | Description |
|----------------|-------------|-------------|
| `infra.GetDefault("defaults.kernel", "version")` | `defaults.kernel.version` | Default version for `mvm kernel pull --type official` |
| `infra.GetDefault("defaults.kernel", "build_jobs")` | `defaults.kernel.build_jobs` | Parallel build jobs (nil = all cores) |
| `infra.GetDefault("defaults.image", "import_format")` | `defaults.image.import_format` | Default import format for images |
| `infra.DefaultFirecrackerCIVersion` | `constants.go` (VM constants section) | CI version used when config lookup fails |
| `infra.SupportedImageExtensions` | `constants.go` (Image & rootfs processing) | File extensions scanned for cached images |
| `infra.ImageImportFormatMap` | `constants.go` (Image & rootfs processing) | Extension → format auto-detection table |
| `infra.HTTPTimeoutKernelDownloadS` | `constants.go` (HTTP / download) | Timeout for kernel tarball download (seconds) |
| `infra.HTTPTimeoutKernelConfigS` | `constants.go` (HTTP / download) | Timeout for kernel config download (seconds) |
| `infra.HTTPTimeoutSha256FetchS` | `constants.go` (HTTP / download) | Timeout for SHA-256 checksum fetch (seconds) |
| `infra.FirecrackerGithubReleasesAPIURL` | `constants.go` (HTTP / download) | GitHub API endpoint for Firecracker releases |
| `infra.FirecrackerGithubDownloadURL` | `constants.go` (HTTP / download) | Base URL for Firecracker release assets |

> **Note:** The kernel config map (`default_configs`) and feature groups (`features`)
> are defined per-kernel in `kernels.yaml`, not as package-level constants.
> Load them at runtime via `kernel.Service.GetSpecsFor()` or by reading `kernels.yaml`
> through the embedded asset manager.

---

*See also: [KERNEL.md](KERNEL.md) for the kernel build workflow,
[CONTEXT.md](../CONTEXT.md) for the Go API patterns.*
