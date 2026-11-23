package inputs_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"mvmctl/internal/lib/model"
	"mvmctl/internal/testutil"
	"mvmctl/pkg/api/inputs"
)

func TestFloatingIPCreateResolvesDefaultAndRefusesReservedSubnetAddresses(t *testing.T) {
	repo := testutil.NewNetworkRepo()
	require.NoError(t, repo.Upsert(t.Context(), &model.NetworkItem{
		ID: "network", Name: "qa", Subnet: "10.90.0.0/24", IPv4Gateway: "10.90.0.1", IsPresent: true, IsDefault: true,
	}))
	cases := map[string]struct {
		address string
		bad     bool
	}{
		"automatic":        {},
		"arbitrary usable": {address: "10.90.0.179"},
		"gateway":          {address: "10.90.0.1", bad: true},
		"network":          {address: "10.90.0.0", bad: true},
		"broadcast":        {address: "10.90.0.255", bad: true},
		"other network":    {address: "10.91.0.2", bad: true},
		"mapped IPv4":      {address: "::ffff:10.90.0.2", bad: true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			item, err := (inputs.FloatingIPCreateInput{Name: "api", IPv4: tc.address}).Resolve(t.Context(), repo)
			if tc.bad {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			if item.ID != "network" {
				t.Fatalf("resolved network = %q, want default network", item.ID)
			}
		})
	}
}
