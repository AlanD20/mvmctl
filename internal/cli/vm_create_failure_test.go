package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/cli"
	"mvmctl/internal/cli/common"
	"mvmctl/internal/infra/event"
	"mvmctl/internal/lib/model"
	"mvmctl/internal/testutil"
	"mvmctl/pkg/api/inputs"
	"mvmctl/pkg/errs"
)

// Rationale: the Go API returns completed items alongside a mixed failure, but
// the CLI's error contract keeps data null. Successful selectors belong in
// recovery details, not a success-shaped payload or an implied rollback.
func TestVMCreateMixedFailureKeepsJSONDataNull(t *testing.T) {
	common.BeginOutput([]string{"--json"})
	t.Cleanup(func() { common.BeginOutput(nil) })
	details := map[string]any{
		"created_vm_ids": []string{"completed-id"},
		"failed_vms":     []map[string]any{{"vm_id": "attempted-id", "name": "batch-2"}},
	}
	operationErr := errs.New(errs.CodeVMCreateFailure, "batch-2: launch failed",
		errs.WithEntity("attempted-id"), errs.WithDetails(details),
	)
	mock := &testutil.MockVMAPI{
		VMCreateFunc: func(
			ctx context.Context, input inputs.VMCreateInput, progress event.OnProgressCallback,
		) ([]*model.VMItem, error) {
			return []*model.VMItem{{ID: "completed-id", Name: "batch"}}, operationErr
		},
	}
	cmd := cli.NewVMCmd(mock, nil)
	create, _, err := cmd.Find([]string{"create"})
	require.NoError(t, err)
	err = create.RunE(create, []string{"batch"})
	require.ErrorIs(t, err, operationErr)
	var output bytes.Buffer
	require.ErrorIs(t, common.FinishOutput(&output, err), operationErr)
	var got common.OutputEnvelope
	require.NoError(t, json.Unmarshal(output.Bytes(), &got))
	want := common.OutputEnvelope{
		SchemaVersion: 1, Status: "error", Data: nil,
		Error: &common.OutputError{
			Code: errs.CodeVMCreateFailure, Class: errs.ClassInternal,
			Message: "batch-2: launch failed", Op: "vm", Entity: "attempted-id",
			Details: map[string]any{
				"created_vm_ids": []any{"completed-id"},
				"failed_vms":     []any{map[string]any{"vm_id": "attempted-id", "name": "batch-2"}},
			},
		},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("failed-create envelope (-want +got):\n%s", diff)
	}
}
