package inputs

import (
	"reflect"
	"testing"

	"mvmctl/internal/lib/model"
)

func TestResolveFeatures(t *testing.T) {
	specFeatures := map[string]model.KernelFeature{
		"kvm":      {Desc: "KVM support", Enforce: map[string]string{"CONFIG_KVM": "y"}},
		"nftables": {Desc: "nftables support", Enforce: map[string]string{"CONFIG_NFTABLES": "y"}},
		"tuntap":   {Desc: "TUN/TAP support", Enforce: map[string]string{"CONFIG_TUN": "y"}},
		"btrfs":    {Desc: "Btrfs support", Enforce: map[string]string{"CONFIG_BTRFS_FS": "y"}},
	}

	tests := []struct {
		name      string
		requested []string
		want      []string
		wantErr   bool
	}{
		{
			name:      "specific features",
			requested: []string{"kvm", "nftables"},
			want:      []string{"kvm", "nftables"},
		},
		{
			name:      "all wildcard",
			requested: []string{"all"},
			want:      []string{"btrfs", "kvm", "nftables", "tuntap"},
		},
		{
			name:      "star wildcard",
			requested: []string{"*"},
			want:      []string{"btrfs", "kvm", "nftables", "tuntap"},
		},
		{
			name:      "kvm first with all",
			requested: []string{"kvm", "all"},
			want:      []string{"kvm", "btrfs", "nftables", "tuntap"},
		},
		{
			name:      "deduplication",
			requested: []string{"kvm", "nftables", "kvm"},
			want:      []string{"kvm", "nftables"},
		},
		{
			name:      "unknown feature",
			requested: []string{"kvm", "unknown"},
			wantErr:   true,
		},
		{
			name:      "empty list",
			requested: []string{},
			want:      nil,
		},
		{
			name:      "nil list",
			requested: nil,
			want:      nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ResolveFeatures(tt.requested, specFeatures)
			if tt.wantErr {
				if err == nil {
					t.Errorf("ResolveFeatures() error = nil, wantErr = true")
				}
				return
			}
			if err != nil {
				t.Errorf("ResolveFeatures() error = %v", err)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ResolveFeatures() = %v, want %v", got, tt.want)
			}
		})
	}
}
