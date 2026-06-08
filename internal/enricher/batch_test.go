package enricher

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSortByDotCount(t *testing.T) {
	tests := []struct {
		name  string
		input []string
		want  []string
	}{
		{
			name:  "already_sorted",
			input: []string{"network", "network.leases"},
			want:  []string{"network", "network.leases"},
		},
		{
			name:  "reverse_order",
			input: []string{"network.leases", "network"},
			want:  []string{"network", "network.leases"},
		},
		{
			name:  "multiple_dots",
			input: []string{"a.b.c", "a", "a.b"},
			want:  []string{"a", "a.b", "a.b.c"},
		},
		{
			name:  "no_dots_preserves_order",
			input: []string{"vm", "kernel", "image"},
			want:  []string{"vm", "kernel", "image"},
		},
		{
			name:  "empty",
			input: []string{},
			want:  []string{},
		},
		{
			name:  "single",
			input: []string{"vm"},
			want:  []string{"vm"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sortByDotCount(tt.input)
			assert.Equal(t, tt.want, got)
		})
	}
}
