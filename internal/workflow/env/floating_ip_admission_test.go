package env

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFloatingIPDeclarationsRejectInvalidAuthorityBeforeEffects(t *testing.T) {
	for name, declaration := range map[string]string{
		"unknown reservation field":     "floating_ip:\n  api:\n    network: cluster\n    owner: controller\n",
		"adopt with allocation":         "floating_ip:\n  api:\n    network: cluster\n    existing: service\n    ipv4: 172.27.0.100\n",
		"grant without lease":           "floating_ip_grant:\n  grant:\n    network: cluster\n    resource: api\n    vms:\n      - controller\n    methods:\n      - method: associate\n",
		"fractional lease":              "floating_ip_grant:\n  grant:\n    network: cluster\n    resource: api\n    vms:\n      - controller\n    methods:\n      - method: renew\n        lease: 1500ms\n",
		"inspect lease":                 "floating_ip_grant:\n  grant:\n    network: cluster\n    resource: api\n    vms:\n      - controller\n    methods:\n      - method: inspect\n        lease: 0s\n",
		"wrong resource type":           "network:\n  cluster:\n    subnet: 172.27.0.0/24\nfloating_ip_grant:\n  grant:\n    network: '@network:cluster'\n    resource: '@network:cluster'\n    vms:\n      - controller\n    methods:\n      - method: inspect\n    depends_on: ['@network:cluster']\n",
		"missing dependency":            "network:\n  cluster:\n    subnet: 172.27.0.0/24\nfloating_ip:\n  api:\n    network: '@network:cluster'\n",
		"ambiguous traffic destination": "traffic_policy:\n  service:\n    source_vm: client\n    destination_vm: controller\n    destination_floating_ip: api\n    destination_network: cluster\n    protocol: tcp\n    port: 6443\n",
		"missing destination network":   "traffic_policy:\n  service:\n    source_vm: client\n    destination_floating_ip: api\n    protocol: tcp\n    port: 6443\n",
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "spec.yaml")
			require.NoError(t, os.WriteFile(path, []byte("version: '1'\n"+declaration), 0600))
			err := Apply(t.Context(), nil, path, nil, nil)
			require.ErrorContains(t, err, "resolve env spec")
			assert.NotContains(t, err.Error(), "operation not initialized")
		})
	}
}
