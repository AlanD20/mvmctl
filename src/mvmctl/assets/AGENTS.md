# mvmctl/assets/ — Bundled Configuration

**Scope:** Static YAML/JSON assets bundled with the package; read at runtime, never mutated
**Status:** Pre-production project — refactoring MUST NOT create legacy migration logic.
**Rule:** Never read directly — access via `core/config.py:load_config()` or `utils/fs.get_assets_dir()`

## STRUCTURE

```
src/mvmctl/assets/
├── _defaults.py              # Master runtime defaults: 220 lines, 24 sections (Python dict)
├── images.yaml               # Image catalog: 7 entries with URLs + convert specs
├── kernels.yaml              # Kernel catalog: build-from-source + prebuilt entries
├── cloud-init.template.yaml  # Jinja2 cloud-init user-data template (98 lines)
└── firecracker.template.json # Firecracker boot JSON template with {placeholder} vars
```

## HOW ACCESSED

```python
# _defaults.py: Direct import by constants.py (zero parse overhead)
from mvmctl.assets._defaults import DEFAULTS  # Used by constants.py:_load_defaults()

# YAML loading goes through core/config.py
from mvmctl.api.config import load_config
from mvmctl.utils.fs import get_assets_dir
config = load_config(get_assets_dir())  # Returns MVMConfig dataclass

# Image/kernel catalog lists
from mvmctl.core.config import load_images_from_yaml, load_kernels_from_yaml
images = load_images_from_yaml(get_assets_dir())   # list[ImageSpec]
kernels = load_kernels_from_yaml(get_assets_dir())  # list[KernelSpec]

# Path to assets dir (inside installed package — NOT under cache)
from mvmctl.utils.fs import get_assets_dir
assets = get_assets_dir()  # Path(__file__).parent.parent / "assets"

# Templates via importlib.resources (required for PyInstaller/Nuitka builds)
import importlib.resources
template_path = importlib.resources.files("mvmctl.assets") / "cloud-init.template.yaml"
template_content = template_path.read_text()  # Used by core/cloud_init.py
```

## FILE SCHEMAS

### `_defaults.py` — Master Runtime Defaults

Pure Python dict (`DEFAULTS`) imported directly by `constants.py:_load_defaults()`.
Zero parse overhead at startup — no YAML/JSON loading required.

Top-level sections map to `MVMConfig` dataclass fields:

| Section | Key fields |
|---------|-----------|
| `firecracker` | `binary`, `versions.full` (v1.15.0), `versions.ci` (v1.15) |
| `vm_defaults` | `vcpu_count` (2), `mem_size_mib` (2048), `ssh_user` (root), `boot_args`, `lsm_flags` |
| `network.defaults` | `name` (default), `subnet` (172.35.0.0/24), `ipv4_gateway` (172.35.0.1) |
| `vm.files` | `kernel_filename`, `rootfs_filename`, `rootfs_basename` |
| `vm.cloud_init` | `seed_path`, `kernel_cmdline_ds`, `final_message`, `iso_name`, `drive_id` |
| `vm.boot` | `console`, `reboot`, `panic`, `pci_off` — boot argument components |
| `vm.network_guest` | `mac_default`, `mac_prefix` (02:FC), `iface` (eth0) |
| `vm.firecracker` | `log_level` (Debug), `drive_cache_type` (Unsafe), `drive_io_engine` (Sync) |
| `vm.logging` | `type` (os), `lines` (50), `follow` (False) |
| `vm.snapshot` | `resume` (True) |
| `vm.limits` | `max_vms` (1000) — hard cap on simultaneous VMs |
| `image.defaults` | `arch` (x86_64), `convert_to` (ext4), `import_format` (auto) |
| `image.compression_extension_map` | Maps .ext4 → .ext4.zst, etc. |
| `image.import_format_map` | Maps .qcow2 → qcow2, .tar → tar-rootfs, etc. |
| `host.system_dirs` | `sysctl_conf_dir`, `sudoers_dir` |
| `host.sbin_paths` | `ip`, `iptables`, `iptables_restore`, `iptables_save`, `sysctl` |
| `host.privileged_binaries` | List of 6 paths requiring privileges |
| `host.required_binaries` | `ip`, `iptables`, `qemu-img` — checked at host init |
| `host.iso_binaries` | `mkisofs`, `genisoimage` — ISO creation tools |
| `host.system_files` | `sudoers_drop_in_template`, `iptables_rules_v4`, `iptables_chains` |
| `http` | `download_chunk_size` (1MB), `max_retries` (3), `retry_delay` (1.0), `backoff` (2.0) |
| `kernel.defaults` | `version` (6.19.9), `arch` (x86_64) |
| `fallbacks` | `fc_ci_version` (1.15), `firecracker_bin`, `kernel_build_jobs` (1), `max_parallel_downloads` (4) |
| `libguestfs` | `launch_timeout` (4), `fallback_root_device` (/dev/sda1), `seed_dir`, `root_indicators` |
| `detectors.weights` | `type_code` (1.0), `label` (0.8), `size` (0.5), `filesystem` (0.7) |
| `detectors.scores` | `ROOT_SCORE` (1.0), `EXCLUDE_SCORE` (-1.0), etc. |
| `detectors.thresholds` | `MIN_ROOT_SIZE_MB` (500), `SIZE_TOO_SMALL_MB` (100) |
| `urls.firecracker` | `github_releases_api`, `github_download_base`, `github_raw_base` |
| `debug` | `enabled` (False), `verbose_errors` (True), `show_tracebacks` (False) |

### `images.yaml` — Image Catalog

Each entry → `ImageSpec` dataclass. `id` becomes the CLI argument to `mvm image fetch`:

```yaml
- id: ubuntu-24.04              # mvm image fetch ubuntu-24.04
  type: ubuntu                  # OS family
  version: "24.04"               # OS version
  name: Ubuntu 24.04             # Human-readable name
  source: https://...            # Download URL
  format: tar-rootfs             # tar-rootfs | qcow2 | squashfs | vhd
  convert_to: ext4               # ext4 | btrfs
  minimum_rootfs_size: 2048      # Resize target after conversion
  sha256: null                   # null = fetch from sha256_url sidecar
  sha256_url: https://...        # URL to SHA256SUMS file
  source_base: https://...      # Base URL for S3 sources (optional)
```

**Available images (7):** `ubuntu-24.04`, `ubuntu-24.04-minimal`, `ubuntu-22.04`, `archlinux`, `debian-bookworm`, `ubuntu-fc`, `alpine-3.21`

| ID | Type | Version | Format | Convert To | Source |
|----|------|---------|--------|------------|--------|
| `ubuntu-24.04` | ubuntu | 24.04 | tar-rootfs | ext4 | cloud-images.ubuntu.com |
| `ubuntu-24.04-minimal` | ubuntu | 24.04 | tar-rootfs | ext4 | cloud-images.ubuntu.com (minimal) |
| `ubuntu-22.04` | ubuntu | 22.04 | tar-rootfs | ext4 | cloud-images.ubuntu.com |
| `archlinux` | archlinux | latest | qcow2 | btrfs | geo.mirror.pkgbuild.com |
| `debian-bookworm` | debian | 12 | qcow2 | ext4 | cloud.debian.org |
| `ubuntu-fc` | firecracker | 24.04 | squashfs | ext4 | spec.ccfc.min S3 |
| `alpine-3.21` | alpine | 3.21 | vhd | ext4 | dl-cdn.alpinelinux.org |

**Adding an image:** Append YAML entry with unique `id`. No code changes needed.

### `kernels.yaml` — Kernel Catalog

Two entries covering both acquisition strategies:

| Key | Type | Version | Source | Output Name | Notes |
|-----|------|---------|--------|-------------|-------|
| `kernel-official` | `official` | 6.19.9 | kernel.org tarball | vmlinux-official | Build-from-source pipeline (`core/kernel.py:build_kernel_pipeline()`). 44 enabled configs including filesystems (BTRFS, EXT4, XFS, SquashFS), VirtIO modules, serial console, network, KVM guest, security (Landlock, BPF, cgroups), PCI. |
| `kernel-firecracker` | `firecracker` | 6.1 | Firecracker CI S3 | vmlinux-fc | Prebuilt vmlinux — no compilation. Uses S3 with `list_url_template` for version discovery. Empty config lists (uses Firecracker's provided config). |

Each entry carries `enabled_configs`, `disabled_configs`, `set_val_configs`, `required_settings` — applied during build patching.

### `firecracker.template.json` — Boot Config Template

Python `str.format()`-style (via `utils/template.py:render_template`). Rendered by `core/config_gen.py:ConfigGenerator`:

```json
{
  "boot-source": {
    "kernel_image_path": "{kernel_image_path}",
    "boot_args": "{boot_args}"
  },
  "drives": {drives},
  "network-interfaces": {network_interfaces},
  "machine-config": {
    "vcpu_count": {vcpu_count},
    "mem_size_mib": {mem_size_mib},
    "smt": false,
    "cpu_template": null
  },
  "cpu-config": null,
  "balloon": null,
  "vsock": null,
  "logger": {logger},
  "metrics": {metrics}
}
```

**Template Placeholders:**

| Placeholder | Type | Description |
|-------------|------|-------------|
| `{kernel_image_path}` | string | Path to vmlinux kernel |
| `{boot_args}` | string | Kernel boot arguments |
| `{drives}` | JSON | Pre-serialized drives array (raw JSON) |
| `{network_interfaces}` | JSON | Pre-serialized network config (raw JSON) |
| `{vcpu_count}` | int | Number of vCPUs |
| `{mem_size_mib}` | int | Memory in MiB |
| `{logger}` | JSON | Pre-serialized logger config (raw JSON) |
| `{metrics}` | JSON | Pre-serialized metrics config (raw JSON) |

`{drives}`, `{network_interfaces}`, `{logger}`, `{metrics}` are pre-serialized JSON strings substituted as raw values (not quoted strings).

### `cloud-init.template.yaml` — Cloud-Init User-Data Template

Jinja2 template rendered by `core/cloud_init.py:write_cloud_init()`. Variables injected at VM creation time:

| Variable | Description | Source |
|----------|-------------|--------|
| `{{ vm_name }}` | VM hostname | VM name parameter |
| `{{ user }}` | SSH user | `_defaults.py` `vm_defaults.ssh_user` |
| `{{ ssh_pub_keys }}` | List of SSH public keys | Key manager |
| `{{ guest_ip }}` | Guest IP address | Network allocation |
| `{{ prefix_len }}` | Network prefix length | CIDR calculation |
| `{{ ipv4_gateway }}` | Gateway IP | Network config |

**Template Sections:**

1. **user_data** — Main cloud-init configuration (hostname, DNS, users, SSH keys, packages, commands)
2. **meta_data** — Instance metadata (`instance-id`, `local-hostname`)
3. **network_config** — Netplan/systemd-networkd static IP configuration
4. **nocloud_cfg** — Datasource configuration (`datasource_list: [NoCloud]`)

**Do not edit directly** — changes affect all newly created VMs. Use `--cloud-init` CLI flag to inject custom user-data per VM.

## ANTI-PATTERNS

| Forbidden | Correct |
|-----------|---------|
| Parse `assets/*.yaml` directly with `yaml.safe_load` | Use `load_config()` or `load_images_from_yaml()` |
| Hardcode URLs from `_defaults.py` in Python | Read via `MVMConfig.urls.*` |
| Edit defaults to change per-VM behavior | CLI flags or `mvm config set` |
| Add secrets or tokens to any YAML file | Environment variables only |
