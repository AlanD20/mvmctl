package vm_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"mvmctl/internal/core/vm"
)

func TestGenerateBatchNames(t *testing.T) {
	tests := []struct {
		name     string
		baseName string
		count    int
		want     []string
	}{
		{
			name:     "single_vm",
			baseName: "test-vm",
			count:    1,
			want:     []string{"test-vm"},
		},
		{
			name:     "two_vms",
			baseName: "test-vm",
			count:    2,
			want:     []string{"test-vm", "test-vm-2"},
		},
		{
			name:     "five_vms",
			baseName: "vm",
			count:    5,
			want:     []string{"vm", "vm-2", "vm-3", "vm-4", "vm-5"},
		},
		// count=0 excluded — panics (make([]string, 0) then names[0] = baseName)
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := vm.GenerateBatchNames(tt.baseName, tt.count)
			assert.Equal(t, tt.want, got)
		})
	}
}
