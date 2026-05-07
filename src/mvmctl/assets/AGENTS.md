# mvmctl/assets/ — Bundled Configuration

**Scope:** Static YAML/JSON assets bundled with the package; read-only templates, never mutated
**Status:** Pre-production project — refactoring MUST NOT create legacy migration logic.
**Rule:** Never read directly — use `AssetManager` (via `importlib.resources`) for all bundled asset access

## STRUCTURE

```
src/mvmctl/assets/
├── __init__.py               # Package marker
├── images.yaml               # Image catalog: 7 entries with URLs + convert specs
├── kernels.yaml              # Kernel catalog: build-from-source + prebuilt entries
├── cloud-init.template.yaml  # Jinja2 cloud-init user-data template (98 lines)
└── firecracker.template.json # Firecracker boot JSON template with {placeholder} vars
```

## HOW ACCESSED

All bundled assets are accessed through `AssetManager` (in `core/_shared/_asset_manager.py`), which wraps `importlib.resources` for reliable access regardless of installation method (pip, Nuitka, PyInstaller):

```python
from mvmctl.core._shared import AssetManager

asset = AssetManager()

# Read YAML catalogs
images_yaml = asset.read_file("images.yaml")     # Used by ImageService
kernels_yaml = asset.read_file("kernels.yaml")   # Used by KernelService

# Read cloud-init template (Jinja2)
cloud_init_template = asset.read_file("cloud-init.template.yaml")
# Used by CloudInitManager in core/cloudinit/_manager.py

# Read firecracker boot config template
fc_template = asset.read_file("firecracker.template.json")

# Check if a file exists
if asset.file_exists("custom.template.yaml"):
    content = asset.read_file("custom.template.yaml")

# List all available asset files
files = asset.list_files()
```

**Key consumers:**

| Asset | Consumer | Method |
|-------|----------|--------|
| `images.yaml` | `core/image/_service.py:ImageService.load_available_images()` | `AssetManager().read_file("images.yaml")` |
| `kernels.yaml` | `core/kernel/_service.py:KernelService.load_kernels_from_yaml()` | `AssetManager().read_file("kernels.yaml")` |
| `cloud-init.template.yaml` | `core/cloudinit/_manager.py:CloudInitManager` | `AssetManager().read_file("cloud-init.template.yaml")` |
| `firecracker.template.json` | *(reference/documentation — config built programmatically by `FirecrackerSpawner.generate()`)* | `AssetManager().read_file("firecracker.template.json")` |

There is no `get_assets_dir()` utility function. The `AssetManager` class is the single entry point for all bundled asset access.

## FILE SCHEMAS

### `images.yaml` — Image Catalog

Each entry → `ImageSpec` dataclass. `id` becomes the CLI argument to `mvm image pull`:

```yaml
- id: ubuntu-24.04              # mvm image pull ubuntu-24.04
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

### `firecracker.template.json` — Boot Config Template (Reference)

This file serves as a structural reference. The actual Firecracker boot config is built **programmatically** by `core/vm/_firecracker.py:FirecrackerSpawner.generate()`, which constructs a `FirecrackerConfigDict` directly in Python — not from this template.

Template structure for reference:

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

Jinja2 template rendered by `core/cloudinit/_manager.py:CloudInitManager`. Variables injected at VM creation time:

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
| Parse `assets/*.yaml` directly with `yaml.safe_load` | Use `AssetManager().read_file()` through domain services |
| Hardcode URLs in Python | Read via `MVMConfig.urls.*` or `get_default()` |
| Edit defaults to change per-VM behavior | CLI flags or `mvm config set` |
| Add secrets or tokens to any YAML file | Environment variables only |
