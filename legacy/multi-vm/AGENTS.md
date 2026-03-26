# Firecracker Multi-VM Setup

**Scope:** Multiple Firecracker microVMs with bridge networking
**Status:** Pre-production project — refactoring MUST NOT create legacy migration logic.

## STRUCTURE

```
multi-vm/
├── config.env           # Default VM resources
├── cloud-init/          # Base cloud-init config
├── setup.sh            # Create bridge + base-rootfs.ext4
├── create-vm.sh        # Create/start individual VMs
├── delete-vm.sh        # Stop/remove VM
├── list-vms.sh         # Show running VMs
└── env/                # VM runtime environments
    ├── base-rootfs.ext4 # Shared base image
    ├── r1/             # VM1 files
    │   ├── rootfs.ext4
    │   ├── firecracker.json
    │   └── *.log
    ├── r2/             # VM2 files
    └── r3/             # VM3 files
```

## WHERE TO LOOK

| Task | Script |
|------|--------|
| Setup bridge/base | `sudo ./setup.sh` |
| Create VM | `sudo ./create-vm.sh <name> [vCPU] [mem] [IP]` |
| List VMs | `sudo ./list-vms.sh` |
| Delete VM | `sudo ./delete-vm.sh <name>` |
| Cleanup all | `sudo ./cleanup.sh` |

## NETWORK

```
Host eth0
   │
   ▼ (NAT MASQUERADE)
fc-br0 (10.20.0.1/24)
   ├── fc-r1-0 ──► r1 (10.20.0.2)
   ├── fc-r2-0 ──► r2 (10.20.0.3)
   └── fc-r3-0 ──► r3 (10.20.0.4)
```

- **Bridge**: fc-br0 (10.20.0.1/24)
- **Guest range**: 10.20.0.2 - 10.20.0.254
- **TAP prefix**: fc-<name>-0

## CONVENTIONS

- VMs created in `env/r*/` directories
- IP assigned automatically or specified: `create-vm.sh vm1 2 2048 10.20.0.50`
- Each VM gets own copy of rootfs from base-rootfs.ext4
- NAT rules auto-managed per VM

## ANTI-PATTERNS

- **Don't delete env/r*/ manually**: Use delete-vm.sh to clean NAT rules
- **Don't hardcode IP ranges**: Check list-vms.sh first
- **Don't run setup.sh after VMs exist**: Destroys base-rootfs.ext4

## COMMANDS

```bash
# Full workflow
sudo ./setup.sh
sudo ./create-vm.sh vm1
sudo ./create-vm.sh vm2 1 512    # 1 vCPU, 512MiB
sudo ./create-vm.sh vm3 2 4096 10.20.0.50  # Custom IP
sudo ./list-vms.sh

# Access
../ssh.sh vm1
ssh -i env/vm.id_rsa root@10.20.0.2

# Cleanup
sudo ./delete-vm.sh vm1
sudo ./cleanup.sh  # Removes all VMs + bridge
```

## NOTES

- **PCI support**: Set `ENABLE_PCI=true` for PCI passthrough
- **LSM flags**: Configurable via `BOOT_ARG_LSM_FLAGS` in config.env
- **VM IDs**: r1, r2, r3... auto-assigned
- **Base image**: All VMs cloned from env/base-rootfs.ext4
