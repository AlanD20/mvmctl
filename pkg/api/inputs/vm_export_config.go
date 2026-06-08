package inputs

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// ── Export Config Types ──
// Matches src/mvmctl/api/inputs/_vm_export_config.py exactly.

// VMExportComputeConfig holds compute resource overrides for export.
// Matches Python's VMExportComputeConfig dataclass:
//
//	@dataclass
//	class VMExportComputeConfig:
//	    vcpus: int | None = None
//	    mem: int | None = None
type VMExportComputeConfig struct {
	VCPUs int `json:"vcpus,omitempty"`
	Mem   int `json:"mem,omitempty"`
}

// VMExportImageConfig holds portable image references for export.
// Matches Python's VMExportImageConfig: all fields are str | None = None.
type VMExportImageConfig struct {
	Type     string `json:"type,omitempty"`
	Arch     string `json:"arch,omitempty"`
	DiskSize string `json:"disk_size,omitempty"`
}

// VMExportKernelConfig holds portable kernel references for export.
// Matches Python's VMExportKernelConfig: all fields are str | None = None.
type VMExportKernelConfig struct {
	Version string `json:"version,omitempty"`
	Arch    string `json:"arch,omitempty"`
	Type    string `json:"type,omitempty"`
}

// VMExportBinaryConfig holds portable binary references for export.
// Matches Python's VMExportBinaryConfig: name: str = "firecracker", version: str | None = None.
type VMExportBinaryConfig struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
}

// VMExportNetworkConfig holds portable network references for export.
// Matches Python's VMExportNetworkConfig: most fields are str | None = None.
type VMExportNetworkConfig struct {
	Name        string `json:"name,omitempty"`
	Subnet      string `json:"subnet,omitempty"`
	IPv4Gateway string `json:"ipv4_gateway,omitempty"`
	NATGateways string `json:"nat_gateways,omitempty"`
	NATEnabled  *bool  `json:"nat_enabled,omitempty"`
	IP          string `json:"ip,omitempty"`
	MAC         string `json:"mac,omitempty"`
}

// VMExportBootConfig holds boot configuration for export.
// Matches:
//
//	@dataclass
//	class VMExportBootConfig:
//	    args: str | None = None
//	    enable_console: bool | None = None
type VMExportBootConfig struct {
	Args          string `json:"args"`
	EnableConsole *bool  `json:"enable_console,omitempty"`
}

// VMExportFirecrackerConfig holds Firecracker feature flags for export.
// Matches Python's VMExportFirecrackerConfig: lsm_flags and cpu_config are str | None.
type VMExportFirecrackerConfig struct {
	EnableAPISocket *bool  `json:"enable_api_socket,omitempty"`
	PCIEnabled      *bool  `json:"pci_enabled,omitempty"`
	LsmFlags        string `json:"lsm_flags"`
	NestedVirt      *bool  `json:"nested_virt,omitempty"`
	CPUConfig       string `json:"cpu_config,omitempty"`
}

// VMExportCloudInitConfig holds cloud-init configuration for export.
// Matches Python's VMExportCloudInitConfig: mode, user, ssh_key are str | None = None.
type VMExportCloudInitConfig struct {
	Mode           string `json:"mode,omitempty"`
	User           string `json:"user,omitempty"`
	SSHKey         string `json:"ssh_key,omitempty"`
	KeepISO        *bool  `json:"keep_iso,omitempty"`
	NocloudNetPort int    `json:"nocloud_net_port,omitempty"`
}

// VMExportConfig is a portable VM configuration for export/import across hosts.
// Uses semantic field references (type, version, name) — NEVER internal IDs.
//
// Matches Python's VMExportConfig dataclass exactly. None values mean
// "use the target system's default at import time" and are omitted from JSON.
//
//	NEVER add: image_id, kernel_id, binary_id, network_id — those are internal.
type VMExportConfig struct {
	SchemaVersion string                    `json:"schema_version"`
	Name          string                    `json:"name"`
	Compute       VMExportComputeConfig     `json:"compute,omitempty"`
	Image         VMExportImageConfig       `json:"image,omitempty"`
	Kernel        VMExportKernelConfig      `json:"kernel,omitempty"`
	Binary        VMExportBinaryConfig      `json:"binary,omitempty"`
	Network       VMExportNetworkConfig     `json:"network,omitempty"`
	Boot          VMExportBootConfig        `json:"boot,omitempty"`
	Firecracker   VMExportFirecrackerConfig `json:"firecracker,omitempty"`
	CloudInit     VMExportCloudInitConfig   `json:"cloud_init,omitempty"`
}

// ToMap serializes to a dictionary, omitting nil/zero values.
// Matches Python's VMExportConfig.to_dict() which calls asdict() + _omit_none().
func (c *VMExportConfig) ToMap() map[string]any {
	data, err := json.Marshal(c)
	if err != nil {
		return map[string]any{
			"schema_version": c.SchemaVersion,
			"name":           c.Name,
		}
	}
	var result map[string]any
	if err := json.Unmarshal(data, &result); err != nil {
		return map[string]any{
			"schema_version": c.SchemaVersion,
			"name":           c.Name,
		}
	}
	return result
}

// ToJSON serializes to formatted JSON bytes (indented, 2 spaces).
func (c *VMExportConfig) ToJSON() ([]byte, error) {
	return json.MarshalIndent(c.ToMap(), "", "  ")
}

// ToJSONFile exports the config to a JSON file, creating parent directories if needed.
// Matches Python's VMExportConfig.to_json_file() exactly.
func (c *VMExportConfig) ToJSONFile(path string) error {
	data, err := c.ToJSON()
	if err != nil {
		return fmt.Errorf("serialize export config: %w", err)
	}
	dir := filepath.Dir(path)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("create export directory: %w", err)
		}
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("write export config: %w", err)
	}
	return nil
}

// ---- Import/Deserialization ----

// FromVMExportConfigJSONFile reads a JSON file and deserializes it into VMExportConfig.
// Matches Python's VMExportConfig.from_json_file() exactly.
func FromVMExportConfigJSONFile(path string) (*VMExportConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("VM config file not found: %s", path)
	}
	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil, fmt.Errorf("invalid JSON in VM config file %s: %w", path, err)
	}
	if parsed == nil {
		return nil, fmt.Errorf("VM config file must be a JSON object: %s", path)
	}
	return fromVMExportConfigMap(parsed), nil
}

// fromVMExportConfigMap deserializes a map into VMExportConfig, filtering unknown fields.
// Matches Python's VMExportConfig.from_dict() which uses __dataclass_fields__ for filtering.
func fromVMExportConfigMap(data map[string]any) *VMExportConfig {
	cfg := &VMExportConfig{
		SchemaVersion: "1.0",
		Binary: VMExportBinaryConfig{
			Name: "firecracker",
		},
	}

	if v, ok := data["schema_version"]; ok {
		if s, ok := v.(string); ok {
			cfg.SchemaVersion = s
		}
	}
	if v, ok := data["name"]; ok {
		if s, ok := v.(string); ok {
			cfg.Name = s
		}
	}

	// Parse nested sub-configs using JSON round-trip for field filtering
	for fieldName, target := range map[string]any{
		"compute":     &cfg.Compute,
		"image":       &cfg.Image,
		"kernel":      &cfg.Kernel,
		"binary":      &cfg.Binary,
		"network":     &cfg.Network,
		"boot":        &cfg.Boot,
		"firecracker": &cfg.Firecracker,
		"cloud_init":  &cfg.CloudInit,
	} {
		if subData, ok := data[fieldName]; ok {
			if subMap, ok := subData.(map[string]any); ok {
				subJSON, err := json.Marshal(subMap)
				if err == nil {
					_ = json.Unmarshal(subJSON, target)
				}
			}
		}
	}

	return cfg
}
