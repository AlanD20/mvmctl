package firewall

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDecodeIntentJournalRejectsPreviousSchema(t *testing.T) {
	binding := testGenerationRecordBinding()
	journal := intentJournal{
		SchemaVersion: 2,
		BootID:        binding.bootID,
		HostNamespace: NamespaceIdentity{Device: binding.namespace.device, Inode: binding.namespace.inode},
		Before:        Intent{Networks: secondaryDurableIntent().Networks},
	}
	raw, err := json.Marshal(journal)
	require.NoError(t, err)

	raw = bytes.ReplaceAll(raw, []byte(`"vm_labels":null,`), nil)
	_, err = decodeIntentJournal(raw)
	require.ErrorContains(t, err, "unsupported firewall intent journal schema")
}
