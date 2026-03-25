# Firecracker Single-VM Setup

**Scope:** Single Firecracker microVM with TAP device and NAT

## STRUCTURE

```
single-vm/
├── config.env           # VM resources (vCPU, memory, disk)
├── cloud-init/
│   ├── user-data       # cloud-init config
│   └── 99-nocloud.cfg  # Data source config
├── setup.sh            # Prepare env/ directory
└── create-vm.sh        # Start Firecracker VM
```

## WHERE TO LOOK

| Task | Script |
|------|--------|
| Change VM resources | `config.env` |
| Customize cloud-init | `cloud-init/user-data` |
| Setup VM (run once) | `sudo ./setup.sh` |
| Start/stop VM | `sudo ./create-vm.sh` / `sudo ./delete-vm.sh` |
| View boot logs | `./logs-vm.sh` |
| View Firecracker logs | `./logs-vm.sh os` |

## NETWORK

```
Host                    Guest
eth0 ──NAT──► fc-tap0 ──► eth0 (10.10.0.2)
      (MASQUERADE)      gw: 10.10.0.1
```

- **Guest IP**: 10.10.0.2
- **Host TAP**: fc-tap0 (10.10.0.1)
- **MAC**: 02:FC:00:00:00:01

## CONVENTIONS

- Generated files in `env/` (rootfs.ext4, firecracker.json, vm.id_rsa)
- Uses `../assets/` for kernel and base image
- Rootfs is copied, not shared (isolated VM)

## ANTI-PATTERNS

- **Don't edit `env/firecracker.json` directly**: It's regenerated
- **Don't run from multi-vm/**: Paths won't resolve
- **Don't forget to delete-vm.sh first**: setup.sh fails if env/ exists

## COMMANDS

```bash
# Full workflow
cd single-vm
sudo ./setup.sh           # Create env/, embed cloud-init
sudo ./create-vm.sh       # Start VM
tail -f env/firecracker.console.log
./ssh.sh                  # SSH as root
sudo ./delete-vm.sh       # Cleanup
```

## NOTES

- **Socket API**: Set `ENABLE_SOCKET=true` in config.env for graceful shutdown
- **Disk size**: Configured in config.env, applied during setup.sh
- **Boot args**: IP configuration hardcoded in create-vm.sh
