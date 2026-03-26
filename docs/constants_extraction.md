# Constants Extraction Plan

**Audited:** 2026-03-25  
**Purpose:** Extract hardcoded magic values into named constants in `constants.py`

---

## BATCH 1: Network Defaults

### 1. Gateway IP Address (10.20.0.1)
| File | Line | Current Value | Proposed Constant |
|------|------|---------------|------------------|
| `src/mvmctl/core/cloud_init.py` | 57 | `"10.20.0.1"` | `FALLBACK_GATEWAY_IP` |
| `src/mvmctl/core/config_gen.py` | 87 | `"10.20.0.1"` | `FALLBACK_GATEWAY_IP` |
| `src/mvmctl/core/config_gen.py` | 148 | `"10.20.0.1"` | `FALLBACK_GATEWAY_IP` |

**Note:** These are fallback defaults when `vm_config.gateway` is not set. Already in defaults.yaml as `network.defaults.gateway: "172.35.0.1"` but cloud-init/config_gen override to 10.20.0.1.

### 2. Network Pool Base (10.20.0.0/16)
| File | Line | Current Value | Proposed Constant |
|------|------|---------------|------------------|
| `src/mvmctl/core/network_manager.py` | 482 | `"10.20.{i}.0/24"` | `FALLBACK_NETWORK_POOL_CIDR` |
| `src/mvmctl/core/network_manager.py` | 486 | `"10.20.0.0/16 pool"` | `FALLBACK_NETWORK_POOL_CIDR` (as string) |

### 3. DNS Servers
| File | Line | Current Value | Proposed Constant |
|------|------|---------------|------------------|
| `src/mvmctl/core/cloud_init.py` | 75 | `["8.8.8.8", "1.1.1.1"]` | `FALLBACK_DNS_NAMESERVERS` |

### 4. Subnet Mask
| File | Line | Current Value | Proposed Constant |
|------|------|---------------|------------------|
| `src/mvmctl/core/config_gen.py` | 88 | `"255.255.255.0"` | `FALLBACK_SUBNET_MASK` |
| `src/mvmctl/core/config_gen.py` | 149 | `"255.255.255.0"` | `FALLBACK_SUBNET_MASK` |

---

## BATCH 2: Firecracker File Names

### 5. Firecracker Log File
| File | Line | Current Value | Proposed Constant |
|------|------|---------------|------------------|
| `src/mvmctl/core/vm_lifecycle.py` | 422 | `"firecracker.log"` | `FALLBACK_FC_LOG_FILENAME` |
| `src/mvmctl/core/config_gen.py` | 187 | `"firecracker.log"` | `FALLBACK_FC_LOG_FILENAME` |
| `src/mvmctl/core/logs.py` | 17 | `"firecracker.log"` | `FALLBACK_FC_LOG_FILENAME` |

### 6. Firecracker Console Log
| File | Line | Current Value | Proposed Constant |
|------|------|---------------|------------------|
| `src/mvmctl/core/vm_lifecycle.py` | 423 | `"firecracker.console.log"` | `FALLBACK_FC_CONSOLE_LOG_FILENAME` |
| `src/mvmctl/core/logs.py` | 16 | `"firecracker.console.log"` | `FALLBACK_FC_CONSOLE_LOG_FILENAME` |

### 7. Firecracker Metrics File
| File | Line | Current Value | Proposed Constant |
|------|------|---------------|------------------|
| `src/mvmctl/core/config_gen.py` | 191 | `"firecracker.metrics"` | `FALLBACK_FC_METRICS_FILENAME` |

### 8. Firecracker API Socket
| File | Line | Current Value | Proposed Constant |
|------|------|---------------|------------------|
| `src/mvmctl/core/vm_lifecycle.py` | 379 | `"firecracker.api.socket"` | `FALLBACK_FC_API_SOCKET_FILENAME` |
| `src/mvmctl/core/firecracker.py` | 256 | `"firecracker.api.socket"` | `FALLBACK_FC_API_SOCKET_FILENAME` |

### 9. Firecracker PID File
| File | Line | Current Value | Proposed Constant |
|------|------|---------------|------------------|
| `src/mvmctl/core/vm_lifecycle.py` | 424 | `"firecracker.pid"` | `FALLBACK_FC_PID_FILENAME` |
| `src/mvmctl/core/vm_lifecycle.py` | 491 | `"firecracker.pid"` | `FALLBACK_FC_PID_FILENAME` |

### 10. Firecracker JSON Config
| File | Line | Current Value | Proposed Constant |
|------|------|---------------|------------------|
| `src/mvmctl/core/vm_lifecycle.py` | 397 | `"firecracker.json"` | `FALLBACK_FC_CONFIG_FILENAME` |
| `src/mvmctl/cli/config.py` | 81 | `"firecracker.json"` | `FALLBACK_FC_CONFIG_FILENAME` |

### 11. Rootfs File Name
| File | Line | Current Value | Proposed Constant |
|------|------|---------------|------------------|
| `src/mvmctl/core/vm_lifecycle.py` | 353 | `"rootfs.ext4"` | `FALLBACK_ROOTFS_FILENAME` |

### 12. Cloud-Init Directory Name
| File | Line | Current Value | Proposed Constant |
|------|------|---------------|------------------|
| `src/mvmctl/core/vm_lifecycle.py` | 360 | `"cloud-init"` | `FALLBACK_CLOUD_INIT_DIRNAME` |

---

## BATCH 3: Kernel Type Strings

### 13. Kernel Type: "firecracker"
| File | Line | Current Value | Proposed Constant |
|------|------|---------------|------------------|
| `src/mvmctl/cli/asset.py` | 194 | `"firecracker"` | `KERNEL_TYPE_FIRECRACKER` |
| `src/mvmctl/cli/asset.py` | 206 | `"firecracker"` | `KERNEL_TYPE_FIRECRACKER` |
| `src/mvmctl/core/kernel.py` | 352 | `"firecracker"` | `KERNEL_TYPE_FIRECRACKER` |
| `src/mvmctl/core/kernel.py` | 909 | `"firecracker"` | `KERNEL_TYPE_FIRECRACKER` |
| `src/mvmctl/core/kernel.py` | 984 | `"firecracker"` | `KERNEL_TYPE_FIRECRACKER` |

### 14. Kernel Type: "official"
| File | Line | Current Value | Proposed Constant |
|------|------|---------------|------------------|
| `src/mvmctl/cli/asset.py` | 222 | `"official"` | `KERNEL_TYPE_OFFICIAL` |
| `src/mvmctl/core/kernel.py` | 458 | `"official"` | `KERNEL_TYPE_OFFICIAL` |
| `src/mvmctl/core/kernel.py` | 644 | `"official"` | `KERNEL_TYPE_OFFICIAL` |
| `src/mvmctl/core/kernel.py` | 670 | `"official"` | `KERNEL_TYPE_OFFICIAL` |
| `src/mvmctl/core/kernel.py` | 752 | `"official"` | `KERNEL_TYPE_OFFICIAL` |

### 15. Kernel Type: "unknown"
| File | Line | Current Value | Proposed Constant |
|------|------|---------------|------------------|
| `src/mvmctl/core/kernel.py` | 793 | `"unknown"` | `KERNEL_TYPE_UNKNOWN` |
| `src/mvmctl/core/kernel.py` | 846 | `"unknown"` | `KERNEL_TYPE_UNKNOWN` |
| `src/mvmctl/core/kernel.py` | 852 | `"unknown"` | `KERNEL_TYPE_UNKNOWN` |

---

## BATCH 4: Firecracker Config Defaults

### 16. Firecracker Log Level
| File | Line | Current Value | Proposed Constant |
|------|------|---------------|------------------|
| `src/mvmctl/core/config_gen.py` | 136 | `"Debug"` | `FALLBACK_FC_LOG_LEVEL` |

### 17. Firecracker Drive Cache Type
| File | Line | Current Value | Proposed Constant |
|------|------|---------------|------------------|
| `src/mvmctl/core/config_gen.py` | 118 | `"Unsafe"` | `FALLBACK_FC_DRIVE_CACHE_TYPE` |

### 18. Firecracker Drive IO Engine
| File | Line | Current Value | Proposed Constant |
|------|------|---------------|------------------|
| `src/mvmctl/core/config_gen.py` | 119 | `"Sync"` | `FALLBACK_FC_DRIVE_IO_ENGINE` |

### 19. Default MAC Prefix (02:FC)
| File | Line | Current Value | Proposed Constant |
|------|------|---------------|------------------|
| `src/mvmctl/core/config_gen.py` | 180 | `"02:FC:00:00:00:01"` | `FALLBACK_GUEST_MAC_DEFAULT` |
| `src/mvmctl/core/network.py` | 815 | `"02:FC:{suffix}"` | `FALLBACK_GUEST_MAC_PREFIX` |

### 20. Network Interface ID
| File | Line | Current Value | Proposed Constant |
|------|------|---------------|------------------|
| `src/mvmctl/core/config_gen.py` | 179 | `"eth0"` | `FALLBACK_GUEST_NETWORK_IFACE` |
| `src/mvmctl/core/cloud_init.py` | 69 | `"eth0"` | `FALLBACK_GUEST_NETWORK_IFACE` |

---

## BATCH 5: Cloud-Init Constants

### 21. Cloud-Init Seed Path
| File | Line | Current Value | Proposed Constant |
|------|------|---------------|------------------|
| `src/mvmctl/core/cloud_init.py` | 151 | `"/var/lib/cloud/seed/nocloud"` | `CLOUD_INIT_SEED_PATH` |

### 22. Cloud-Init DS String
| File | Line | Current Value | Proposed Constant |
|------|------|---------------|------------------|
| `src/mvmctl/core/config_gen.py` | 167 | `"ds=nocloud;s=file:///var/lib/cloud/seed/nocloud/"` | `CLOUD_INIT_KERNEL_CMDLINE_DS` |

### 23. Cloud-Init Final Message
| File | Line | Current Value | Proposed Constant |
|------|------|---------------|------------------|
| `src/mvmctl/core/cloud_init.py` | 126 | `"mvm cloud-init done"` | `CLOUD_INIT_FINAL_MESSAGE` |

### 24. Cloud-Init Disable Snapd Command
| File | Line | Current Value | Proposed Constant |
|------|------|---------------|------------------|
| `src/mvmctl/core/cloud_init.py` | 122 | `"systemctl disable --now snapd.socket 2>/dev/null || true"` | `CLOUD_INIT_DISABLE_SNAPD_CMD` |

---

## BATCH 6: Kernel Boot Args

### 25. Default Boot Args Components
| File | Line | Current Value | Proposed Constant |
|------|------|---------------|------------------|
| `src/mvmctl/core/config_gen.py` | 159 | `"console=ttyS0"` | `FALLBACK_BOOT_CONSOLE` |
| `src/mvmctl/core/config_gen.py` | 160 | `"reboot=k"` | `FALLBACK_BOOT_REBOOT` |
| `src/mvmctl/core/config_gen.py` | 161 | `"panic=1"` | `FALLBACK_BOOT_PANIC` |
| `src/mvmctl/core/config_gen.py` | 147 | `"pci=off"` | `FALLBACK_BOOT_PCI_OFF` |

---

## BATCH 7: Timeouts/Poll Intervals

### 26. Shutdown Poll Step
| File | Line | Current Value | Proposed Constant |
|------|------|---------------|------------------|
| `src/mvmctl/core/vm_lifecycle.py` | 230 | `time.sleep(0.1)` | `FIRECRACKER_SHUTDOWN_POLL_INTERVAL_S` |

### 27. Log Follow Sleep
| File | Line | Current Value | Proposed Constant |
|------|------|---------------|------------------|
| `src/mvmctl/core/logs.py` | 96 | `time.sleep(0.3)` | `LOG_FOLLOW_POLL_INTERVAL_S` |

---

## BATCH 8: HTTP Download Timeouts

### 28. Kernel Download Timeout
| File | Line | Current Value | Proposed Constant |
|------|------|---------------|------------------|
| `src/mvmctl/core/kernel.py` | 292 | `timeout=600` | `HTTP_TIMEOUT_KERNEL_DOWNLOAD_S` |

### 29. Kernel Config Download Timeout
| File | Line | Current Value | Proposed Constant |
|------|------|---------------|------------------|
| `src/mvmctl/core/kernel.py` | 370 | `timeout=60` | `HTTP_TIMEOUT_KERNEL_CONFIG_S` |

### 30. SHA256 Fetch Timeout
| File | Line | Current Value | Proposed Constant |
|------|------|---------------|------------------|
| `src/mvmctl/core/kernel.py` | 607 | `timeout=30` | `HTTP_TIMEOUT_SHA256_FETCH_S` |
| `src/mvmctl/core/kernel.py` | 619 | `timeout=30` | `HTTP_TIMEOUT_SHA256_FETCH_S` |
| `src/mvmctl/core/kernel.py` | 923 | `timeout=30` | `HTTP_TIMEOUT_SHA256_FETCH_S` |
| `src/mvmctl/core/image.py` | 418 | `timeout=30` | `HTTP_TIMEOUT_SHA256_FETCH_S` |
| `src/mvmctl/core/image.py` | 442 | `timeout=30` | `HTTP_TIMEOUT_SHA256_FETCH_S` |
| `src/mvmctl/core/binary_manager.py` | 116 | `timeout=30` | `HTTP_TIMEOUT_SHA256_FETCH_S` |
| `src/mvmctl/core/binary_manager.py` | 161 | `timeout=30` | `HTTP_TIMEOUT_SHA256_FETCH_S` |

### 31. Firecracker Binary Download Timeout
| File | Line | Current Value | Proposed Constant |
|------|------|---------------|------------------|
| `src/mvmctl/core/binary_manager.py` | 179 | `timeout=300` | `HTTP_TIMEOUT_FIRECRACKER_DOWNLOAD_S` |

### 32. Kernel SHA256 Sidecar Download Timeout
| File | Line | Current Value | Proposed Constant |
|------|------|---------------|------------------|
| `src/mvmctl/core/kernel.py` | 956 | `timeout=15` | `HTTP_TIMEOUT_SHA256_SIDECAR_S` |

### 33. Firecracker CI Kernel Download Timeout
| File | Line | Current Value | Proposed Constant |
|------|------|---------------|------------------|
| `src/mvmctl/core/kernel.py` | 973 | `timeout=300` | `HTTP_TIMEOUT_FC_KERNEL_DOWNLOAD_S` |

---

## Summary

| Batch | Category | Count |
|-------|----------|-------|
| 1 | Network Defaults | 4 constants, 7 occurrences |
| 2 | Firecracker File Names | 8 constants, 11 occurrences |
| 3 | Kernel Type Strings | 3 constants, 11 occurrences |
| 4 | Firecracker Config Defaults | 4 constants, 6 occurrences |
| 5 | Cloud-Init Constants | 4 constants, 4 occurrences |
| 6 | Kernel Boot Args | 4 constants, 5 occurrences |
| 7 | Timeouts/Poll Intervals | 2 constants, 2 occurrences |
| 8 | HTTP Download Timeouts | 6 constants, 14 occurrences |
| **TOTAL** | | **35 constants, 60 occurrences** |

---

## Extraction Order

1. **Batch 3** (Kernel Type Strings) — Simplest, no dependencies
2. **Batch 2** (File Names) — Simple string constants
3. **Batch 4** (FC Config Defaults) — String constants for FC API
4. **Batch 1** (Network Defaults) — IP addresses and DNS
5. **Batch 5** (Cloud-Init) — Path and command strings
6. **Batch 6** (Boot Args) — Kernel command line components
7. **Batch 7** (Timeouts) — Numeric values
8. **Batch 8** (HTTP Timeouts) — Numeric values with clear patterns
