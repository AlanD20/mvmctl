# mvmctl/assets/ — Bundled Configuration

**Scope:** Static YAML/JSON assets bundled with the package; read at runtime, never mutated
**Status:** Pre-production project — refactoring MUST NOT create legacy migration logic.
**Rule:** Never read directly — use `constants.py` for runtime defaults, `get_assets_dir()` for bundled YAML, and `importlib.resources` for templates

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

```python
# Runtime defaults live in constants.py (OVERRIDABLE_DEFAULTS dict, not a separate file)
from mvmctl.constants import get_default

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
| Hardcode URLs in Python | Read via `MVMConfig.urls.*` or `get_default()` |
| Edit defaults to change per-VM behavior | CLI flags or `mvm config set` |
| Add secrets or tokens to any YAML file | Environment variables only |
