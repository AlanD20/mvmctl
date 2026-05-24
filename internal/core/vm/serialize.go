package vm

import (
	"encoding/json"

	"mvmctl/internal/infra/model"
)

// MarshalSSHKeys serializes ssh_keys to a JSON string for DB storage.
// Matches Python: json.dumps(vm.ssh_keys) when vm.ssh_keys defaults to [] (non-nil).
func MarshalSSHKeys(keys []string) (*string, error) {
	if len(keys) == 0 {
		s := "[]"
		return &s, nil
	}
	data, err := json.Marshal(keys)
	if err != nil {
		return nil, err
	}
	s := string(data)
	return &s, nil
}

// MarshalVolumeIDs serializes volume_ids to a JSON string for DB storage.
func MarshalVolumeIDs(ids []string) (*string, error) {
	if ids == nil {
		return nil, nil
	}
	data, err := json.Marshal(ids)
	if err != nil {
		return nil, err
	}
	s := string(data)
	return &s, nil
}

// MarshalCPUConfig serializes cpu_config to a JSON string for DB storage.
func MarshalCPUConfig(cfg *model.CpuConfig) (*string, error) {
	if cfg == nil {
		return nil, nil
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		return nil, err
	}
	s := string(data)
	return &s, nil
}

// UnmarshalSSHKeys deserializes ssh_keys from a DB JSON string.
func UnmarshalSSHKeys(s string) ([]string, error) {
	if s == "" {
		return []string{}, nil
	}
	var keys []string
	if err := json.Unmarshal([]byte(s), &keys); err != nil {
		return nil, err
	}
	return keys, nil
}

// UnmarshalVolumeIDs deserializes volume_ids from a DB JSON string.
func UnmarshalVolumeIDs(s *string) ([]string, error) {
	if s == nil || *s == "" {
		return nil, nil
	}
	if *s == "[]" {
		return []string{}, nil
	}
	var ids []string
	if err := json.Unmarshal([]byte(*s), &ids); err != nil {
		return nil, err
	}
	return ids, nil
}

// UnmarshalCPUConfig deserializes cpu_config from a DB JSON string.
func UnmarshalCPUConfig(s *string) (*model.CpuConfig, error) {
	if s == nil || *s == "" {
		return nil, nil
	}
	var cfg model.CpuConfig
	if err := json.Unmarshal([]byte(*s), &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}
