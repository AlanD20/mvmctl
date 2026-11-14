package model_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"mvmctl/internal/lib/model"
)

func TestIsValidVsockGuestCID(t *testing.T) {
	tests := map[string]struct {
		cid  int
		want bool
	}{
		"negative":      {cid: -1, want: false},
		"hypervisor":    {cid: 0, want: false},
		"host":          {cid: 2, want: false},
		"minimum":       {cid: model.MinVsockGuestCID, want: true},
		"maximum":       {cid: int(model.MaxVsockGuestCID), want: true},
		"above maximum": {cid: int(model.MaxVsockGuestCID) + 1, want: false},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tc.want, model.IsValidVsockGuestCID(tc.cid))
		})
	}
}
