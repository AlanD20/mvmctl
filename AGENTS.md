# Firecracker MicroVM Setup

**Generated:** 2025-03-22
**Language:** Bash (Shell)
**Stack:** Firecracker MicroVM, KVM, cloud-init

## OVERVIEW

Shell-based infrastructure for running lightweight Firecracker microVMs. Two workflows:
- **single-vm/**: One VM with TAP + NAT
- **multi-vm/**: Multiple VMs with bridge + NAT

## STRUCTURE

```
./
├── assets/              # Downloaded binaries, kernels, images
│   ├── bin/firecracker
│   ├── kernels/vmlinux
│   └── images/*.ext4
├── single-vm/           # Single VM setup
│   ├── setup.sh        # Prepare rootfs + network
│   └── create-vm.sh    # Start VM
└── multi-vm/            # Multi-VM setup
    ├── setup.sh        # Bridge + base rootfs
    ├── create-vm.sh    # Create/destroy VMs
    └── env/            # VM runtime environments
        └── r1, r2, r3/ # Individual VM configs
```

## WHERE TO LOOK

| Task | Location | Script |
|------|----------|--------|
| First-time setup | Root | `environment_setup.sh` |
| Download binaries | assets/ | `download-assets.sh` |
| Build kernel | assets/ | `build-kernel.sh` |
| Run single VM | single-vm/ | `setup.sh` → `create-vm.sh` |
| Run multiple VMs | multi-vm/ | `setup.sh` → `create-vm.sh` |
| Connect to VM | Root | `ssh.sh <vm_name>` |
| View logs | */ | `logs-vm.sh <vm_name>` |

## ENTRY POINTS

**No single main script. Three-phase execution:**

1. **Environment**: `./environment_setup.sh` (one-time system config)
2. **Assets**: `cd assets && ./download-assets.sh` (download bins/images)
3. **Workflow** (choose one):
   - `cd single-vm && sudo ./setup.sh && sudo ./create-vm.sh`
   - `cd multi-vm && sudo ./setup.sh && sudo ./create-vm.sh vm1`

## CONVENTIONS

- Scripts require `sudo` for KVM/networking operations
- Config via `config.env` in each directory
- Cloud-init in `cloud-init/user-data`
- VM logs: `env/*.log`, console: `env/*.console.log`
- Generated files in `env/` (gitignored)

## ANTI-PATTERNS

- **Never run scripts from wrong directory**: Scripts use relative paths (`../assets/`)
- **Never skip setup.sh**: Creates required cloud-init ISO and network
- **Don't use qcow2 directly**: Must extract raw filesystem (see custom-images.md)
- **Don't forget sudo**: KVM and network ops require root

## COMMANDS

```bash
# Quick start (single VM)
sudo ./environment_setup.sh
cd assets && sudo ./download-assets.sh
cd ../single-vm && sudo ./setup.sh && sudo ./create-vm.sh

# Multi-VM workflow
cd ../multi-vm && sudo ./setup.sh
sudo ./create-vm.sh vm1       # auto IP
sudo ./create-vm.sh vm2 1 512 # 1 vCPU, 512MiB
sudo ../list-vms.sh
sudo ./delete-vm.sh vm1

# SSH to VM
./ssh.sh vm1                  # from root
ssh -i env/vm.id_rsa root@10.20.0.2
```

## NOTES

- **Image format**: Firecracker needs raw filesystem, not partitioned disk
- **Networking**: single-vm uses fc-tap0 (10.10.0.x), multi-vm uses fc-br0 (10.20.0.x)
- **Boot time**: Wait 30-60s after create-vm.sh before SSH
- **Kernel**: Default uses minimal kernel; custom via `build-kernel.sh`
