from __future__ import annotations

DEFAULTS: dict[str, object] = {
    "firecracker": {
        "binary": "/usr/local/bin/firecracker",
        "versions": {
            "full": "v1.15.0",
            "ci": "v1.15",
        },
    },
    "vm_defaults": {
        "vcpu_count": 1,
        "mem_size_mib": 512,
        "ssh_user": "root",
        "user_password": "password",
        "root_fs_type": "ext4",
        "network_interface": "eth0",
        "boot_args": "console=ttyS0 reboot=k panic=1 net.ifnames=0 rw rootwait",
        "disk_size": "2G",  # FIXME: remove this
        "enable_api_socket": True,
        "enable_pci": False,
        "enable_logging": True,
        "enable_metrics": False,
        "enable_console": True,
        "lsm_flags": "landlock,lockdown,yama,integrity,selinux,bpf",
    },
    "network": {
        "defaults": {
            "name": "net",
            "subnet": "172.27.0.0/24",
            "ipv4_gateway": "172.27.0.1",
        },
    },
    "vm": {
        "files": {
            "kernel_filename": "vmlinux",
            "rootfs_filename": "rootfs.ext4",
            "rootfs_basename": "rootfs",
        },
        "cloud_init": {
            "seed_path": "/var/lib/cloud/seed/nocloud",
            "final_message": "mvm cloud-init done",
            "disable_snapd_cmd": "systemctl disable --now snapd.socket 2>/dev/null || true",
            "dirname": "cloud-init",
            "iso_name": "cloud-init.iso",
            "iso_volume_label": "cidata",
            "required_iso_tool": "cloud-localds",
        },
        "boot": {
            "console": "console=ttyS0",
            "reboot": "reboot=k",
            "panic": "panic=1",
            "pci_off": "pci=off",
        },
        "network_guest": {
            "mac_default": "02:FC:00:00:00:01",
            "mac_prefix": "02:FC",
            "iface": "eth0",
            "boot_mode": "off",
        },
        "firecracker": {
            "log_level": "Debug",
            "drive_cache_type": "Unsafe",
            "drive_io_engine": "Sync",
        },
        "firecracker_bin_name": "firecracker",
        "logging": {
            "type": "os",
            "lines": 50,
            "follow": False,
        },
        "snapshot": {
            "resume": True,
        },
        "limits": {
            "max_vms": 1000,
        },
    },
    "image": {
        "defaults": {
            "arch": "x86_64",
            "convert_to": "ext4",
            "import_format": "auto",
            "import_size_mib": 2048,
            "supported_extensions": [
                ".ext4",
                ".btrfs",
                ".img",
                ".raw",
                ".ext4.zst",
                ".btrfs.zst",
            ],
            "compression_extension_map": {
                ".ext4": ".ext4.zst",
                ".btrfs": ".btrfs.zst",
                ".img": ".img.zst",
                ".raw": ".raw.zst",
            },
            "import_format_map": {
                ".qcow2": "qcow2",
                ".raw": "raw",
                ".img": "raw",
                ".tar": "tar-rootfs",
                ".tar.gz": "tar-rootfs",
                ".tar.xz": "tar-rootfs",
                ".tgz": "tar-rootfs",
            },
        },
        "remote": {
            "version_limit": 5,
        },
        "shrink_safety_margin": 1.01,
        "ratio_min": 1.0,
        "runtime_buffer_mb": 160,  # Buffer in MB added to shrunk size for boot overhead
    },
    "host": {
        "system_dirs": {
            "sysctl_conf_dir": "/etc/sysctl.d",
            "sudoers_dir": "/etc/sudoers",
        },
        "sbin_paths": {
            "ip": "/usr/sbin/ip",
            "iptables": "/usr/sbin/iptables",
            "iptables_restore": "/usr/sbin/iptables-restore",
            "iptables_save": "/usr/sbin/iptables-save",
            "sysctl": "/usr/sbin/sysctl",
        },
        "privileged_binaries": {
            "/usr/sbin/ip": "iproute2",
            "/usr/sbin/iptables": "iptables",
            "/usr/sbin/iptables-save": "iptables",
            "/usr/sbin/sysctl": "procps",
            "/usr/sbin/modprobe": "kmod",
        },
        "required_binaries": [
            "ip",
            "iptables",
            "qemu-img",
            "ssh-keygen",
            "tar",
            "mkfs.ext4",
            "blkid",
            "sfdisk",
            "dumpe2fs",
            "modprobe",
            "lsmod",
            "groupadd",
            "usermod",
            "groupdel",
            "visudo",
        ],
        "iso_binaries": [
            "cloud-localds",
        ],
        "system_files": {
            "sudoers_drop_in_template": "/etc/sudoers.d/{cli_name}",
            "iptables_rules_v4": "/etc/iptables/rules.v4",
            "iptables_chains": [
                {
                    "name": "MVM-FORWARD",
                    "table": "filter",
                    "jump_from": "FORWARD",
                },
                {
                    "name": "MVM-POSTROUTING",
                    "table": "nat",
                    "jump_from": "POSTROUTING",
                },
                {
                    "name": "MVM-NOCLOUDNET-INPUT",
                    "table": "filter",
                    "jump_from": "INPUT",
                },
            ],
        },
    },
    "http": {
        "download_chunk_size": 1048576,
        "download_max_retries": 3,
        "download_retry_delay": 1.0,
        "download_retry_backoff": 2.0,
    },
    "kernel": {
        "defaults": {
            "version": "6.19.9",
            "arch": "x86_64",
        },
    },
    "fallbacks": {
        "fc_ci_version": "1.15",
        "firecracker_bin": "firecracker",
        "kernel_build_jobs": 1,
        "max_parallel_downloads": 4,
    },
    "libguestfs": {
        "launch_timeout": 4,
        "fallback_root_device": "/dev/sda1",
        "seed_dir": "/var/lib/cloud/seed/nocloud",
        "root_indicators": [
            "/etc/os-release",
            "/etc/fstab",
        ],
    },
    "urls": {
        "firecracker": {
            "github_releases_api": "https://api.github.com/repos/firecracker-microvm/firecracker/releases",
            "github_download_base": "https://github.com/firecracker-microvm/firecracker/releases/download",
            "github_raw_base": "https://raw.githubusercontent.com/firecracker-microvm/firecracker/main",
        },
    },
    "detectors": {
        "weights": {
            "type_code": 1.0,
            "label": 0.8,
            "size": 0.5,
            "filesystem": 0.7,
        },
        "scores": {
            "ROOT_SCORE": 1.0,
            "EXCLUDE_SCORE": -1.0,
            "NEUTRAL_SCORE": 0.0,
            "MBR_LINUX_SCORE": 0.5,
            "LABEL_ROOT_SCORE": 1.0,
            "LABEL_EXCLUDE_SCORE": -0.5,
            "SIZE_LARGEST_SCORE": 0.5,
            "SIZE_ROOT_SCORE": 0.3,
            "SIZE_TOO_SMALL_SCORE": -0.5,
        },
        "thresholds": {
            "MIN_ROOT_SIZE_MB": 500,
            "SIZE_TOO_SMALL_MB": 100,
        },
    },
    "debug": {
        "enabled": False,
        "verbose_errors": True,
        "show_tracebacks": False,
    },
}
