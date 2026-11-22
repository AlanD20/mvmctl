package firewall

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDecodeIntentJournalRejectsOldTerminalSchema(t *testing.T) {
	binding := testGenerationRecordBinding()
	journal := intentJournal{
		SchemaVersion: 1,
		BootID:        binding.bootID,
		HostNamespace: NamespaceIdentity{Device: binding.namespace.device, Inode: binding.namespace.inode},
		Before:        Intent{Networks: secondaryDurableIntent().Networks},
	}
	raw, err := json.Marshal(journal)
	require.NoError(t, err)

	_, err = decodeIntentJournal(raw)
	require.ErrorContains(t, err, "unsupported firewall intent journal schema")
}
