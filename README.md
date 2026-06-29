# mvmctl (mvm)

> **Container speed, VM isolation.**

[![CI](https://github.com/AlanD20/mvmctl/actions/workflows/ci.yml/badge.svg)](https://github.com/AlanD20/mvmctl/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
[![Go 1.26.3](https://img.shields.io/badge/go-1.26.3-blue)](https://go.dev/)

**mvmctl** is a CLI tool for running lightweight VMs — fast enough to replace containers for development, isolated enough to trust with real workloads. Built on [Firecracker](https://github.com/firecracker-microvm/firecracker), the same microVM technology that powers AWS Lambda and Fargate.

Think "Docker for VMs" — one command to create, start, and connect.

- ⚡ **Fast boot times** — VMs boot in 2-4 seconds (loop-mount) or 9-14 seconds (guestfs), so you iterate at container speed with VM-grade isolation
- 🔥 **Powered by Firecracker** — AWS's battle-tested microVM technology, the engine behind Lambda and Fargate
- 🛡️ **Secure by default** — hardware-level isolation with KVM
- 📦 **Single binary** — one statically-linked Go binary with no language runtime dependencies. Calls standard Linux utilities (ip, sudo, iptables/nftables, losetup, mount)
- 🖼️ **Image support** — ready-to-use images for Ubuntu, Debian, Arch, Alpine, and more. Import your own base images
- 💾 **Volumes & persistence** — create, attach, resize, and detach persistent data disks that survive VM lifecycles
- ⚙️ **Custom kernels** — download pre-built Firecracker kernels or build official kernels with custom features (KVM, nftables)
- 🎯 **Simple CLI** — one command to create, start, and SSH into a VM
- 🖥️ **Console access** — interactive serial console without SSH via `mvm console`
- 📋 **Environment as code** — provision full VM topologies from a YAML spec: networks, keys, images, kernels, VMs, and post-boot provisioning via `mvm env apply`
- 🔌 **Vsock agent** — run commands inside VMs instantly via a lightweight embedded guest agent (`mvm exec`), no SSH daemon required
- 📁 **File transfer** — copy files between host and VM over vsock with `mvm cp`, no guest dependencies needed
- 🧩 **Atomic batch creation** — spin up multiple VMs as a unit with `--count N --atomic`. All succeed or all roll back
- 🐳 **Container-like ergonomics** — `mvm vm create myvm --image ubuntu:24.04 && mvm ssh myvm` works like `docker run -it ubuntu bash`, but with real VM isolation

---

## Table of Contents

- [Prerequisites](#prerequisites)
- [Installation](#installation)
- [Quick Start](#quick-start)
- [Common Tasks](#common-tasks)
- [Documentation](#documentation)
- [Troubleshooting](#troubleshooting)
- [Contributing](#contributing)
- [License](#license)

---

## Prerequisites

- **Linux** (x86_64 or aarch64) with KVM access (`/dev/kvm`):
  ```bash
  sudo usermod -aG kvm $USER
  # Log out and back in
  ```
- **System packages:**

  Ubuntu/Debian:
  ```bash
  sudo apt-get install -y iproute2 iptables nftables qemu-utils e2fsprogs util-linux procps kmod openssh-client tar sudo passwd fakeroot
  ```
  Arch Linux:
  ```bash
  sudo pacman -S --needed iproute2 iptables nftables qemu-img e2fsprogs util-linux procps-ng kmod openssh tar sudo shadow fakeroot
  ```
  Optional (for ISO cloud-init mode only): `cloud-image-utils` (Ubuntu/Debian) or `cloud-utils` (Arch)
- **Root access (one-time):** run `mvm host init` once to create the `mvm` group and a sudoers drop-in; normal `mvm` commands require no `sudo` after that
- **Environment variables:** Configure runtime behavior via `MVM_*` variables. See [docs/REFERENCES.md](docs/REFERENCES.md#environment-variables) for the full list.

---

## Installation

### 1. Download prebuilt binary (recommended)

You may head over to [Releases](https://github.com/AlanD20/mvmctl/releases) page to get the desired package for your distro or you may simply get the static and standalone binary itself:

```bash
mkdir -p ~/.local/bin
curl -L -o ~/.local/bin/mvm https://github.com/AlanD20/mvmctl/releases/latest/download/mvm
chmod +x ~/.local/bin/mvm
mvm --help
```

> Make sure `~/.local/bin` is in your `$PATH`. Most modern Linux distros include it by default.

### 2. Build from source

```bash
git clone https://github.com/AlanD20/mvmctl
cd mvmctl
./scripts/build.sh release --output ~/.local/bin/mvm
mvm --help
```

See [docs/REFERENCES.md](docs/REFERENCES.md) for the complete command reference with all flags, options, and selectors.

---

## Quick Start

```bash
# 1. One-time system setup (interactive, handles sudo)
mvm init

# 2. Log out and back in, then download a kernel and image
mvm kernel pull --type firecracker --default
mvm image pull ubuntu:24.04 --default

# 3. Create and connect to a VM
mvm vm create myvm --image ubuntu:24.04 --vcpu 2 --mem 2G --disk-size 20G
mvm exec myvm                    # vsock agent — no SSH needed
```

That's it. When you're done: `mvm vm rm myvm`.

---

## Common Tasks

| Task | Command | Learn more |
|------|---------|-----------|
| Create a VM | `mvm vm create myvm --image ubuntu:24.04` | [REFERENCES.md](docs/REFERENCES.md#mvm-vm) |
| Run a command inside a VM | `mvm exec myvm -- ls -la` | [REFERENCES.md](docs/REFERENCES.md#mvm-exec) |
| Copy files to/from a VM | `mvm cp ./file.txt myvm:/root/` | [REFERENCES.md](docs/REFERENCES.md#mvm-cp) |
| SSH into a VM | `mvm ssh myvm` | [REFERENCES.md](docs/REFERENCES.md#mvm-ssh) |
| List running VMs | `mvm vm ps` | [REFERENCES.md](docs/REFERENCES.md#mvm-vm) |
| Download an image | `mvm image pull ubuntu:24.04` | [REFERENCES.md](docs/REFERENCES.md#mvm-image) |
| Create a network | `mvm network create mynet --subnet 10.0.0.0/24` | [REFERENCES.md](docs/REFERENCES.md#mvm-network) |
| Create a snapshot | `mvm snapshot create myvm --name daily` | [REFERENCES.md](docs/REFERENCES.md#mvm-snapshot) |
| Provision from YAML | `mvm env apply my-env.yaml` | [ENV_SPEC_REFERENCE.md](docs/ENV_SPEC_REFERENCE.md) |
| Configure settings | `mvm config set defaults.vm vcpu_count 4` | [REFERENCES.md](docs/REFERENCES.md#mvm-config) |

> **Aliases:** `net` for `network`, `img` for `image`, `vol` for `volume`, `ss` for `snapshot`, `mvm bin` for `mvm binary`. Every `list` accepts `ls`; every `remove` accepts `rm`, `delete`, `del`.

---

## Documentation

| Document | What you'll find |
|----------|-----------------|
| [docs/REFERENCES.md](docs/REFERENCES.md) | Complete command reference, selectors, config, env vars, cloud-init |
| [docs/ENV_SPEC_REFERENCE.md](docs/ENV_SPEC_REFERENCE.md) | Full YAML spec for `mvm env` — all fields, types, examples |
| [docs/TROUBLESHOOTING.md](docs/TROUBLESHOOTING.md) | Common issues, firewall problems, nocloud-net failures |
| [docs/DEPENDENCIES.md](docs/DEPENDENCIES.md) | System dependencies by distro package names |
| [docs/KERNEL.md](docs/KERNEL.md) | Building kernels for Firecracker |
| [docs/RUNTIME.md](docs/RUNTIME.md) | Runtime internals: provisioning backends, firewall |

---

## Troubleshooting

| Issue | Solution |
|-------|----------|
| **Permission denied: /dev/kvm** | Missing: `sudo modprobe kvm && sudo modprobe kvm_intel` (or `kvm_amd`). Unreadable: `sudo usermod -aG kvm $USER`, log out/back in |
| **VM won't boot / SSH times out** | Cloud-init takes 30-60s. Watch with `mvm logs myvm --follow` |
| **nocloud-net server failed** | Port range exhausted. Check: `sudo ss -tlnp \| grep -E ':(8[0-9]{3}\|9[0-9]{3})'` |

See [docs/TROUBLESHOOTING.md](docs/TROUBLESHOOTING.md) for the full guide.

---

## Contributing

See [.github/CONTRIBUTING.md](.github/CONTRIBUTING.md) for the full guide: development setup, project structure, running tests, commit conventions, and the PR process.

---

## License

MIT -- see [LICENSE](LICENSE).
