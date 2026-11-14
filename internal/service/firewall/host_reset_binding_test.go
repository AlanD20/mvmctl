package firewall

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHostResetGenerationBindingKeepsPriorBootHistorySeparateFromCurrentAuthority(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		priorBoot        bool
		foreignNamespace bool
		intentBoot       bool
		intentNamespace  bool
		valid            bool
	}{
		"current exact":              {valid: true},
		"current foreign namespace":  {foreignNamespace: true},
		"prior exact":                {priorBoot: true, valid: true},
		"prior mismatched boot":      {priorBoot: true, intentBoot: true},
		"prior mismatched namespace": {priorBoot: true, intentNamespace: true},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			current := testGenerationRecordBinding()
			binding := current
			if test.priorBoot {
				binding.bootID = "22345678-1234-4abc-8def-1234567890ab"
			}
			if test.foreignNamespace {
				binding.namespace.inode++
			}
			record := mustCommittedGenerationRecord(t, binding,
				generationRecordIdentity{empty: true, id: testFirewallGenerationA})
			journal := intentJournal{BootID: binding.bootID,
				HostNamespace: NamespaceIdentity{Device: binding.namespace.device, Inode: binding.namespace.inode}}
			if test.intentBoot {
				journal.BootID = current.bootID
			}
			if test.intentNamespace {
				journal.HostNamespace.Inode++
			}
			err := admitHostResetGenerationBinding(record, journal, current)
			if test.valid {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}
