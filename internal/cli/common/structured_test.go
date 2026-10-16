package common_test

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/cli/common"
	"mvmctl/pkg/errs"
)

func TestFinishOutputDistinguishesPartialResultFromFailedResult(t *testing.T) {
	tests := []struct {
		name       string
		record     func(any)
		wantStatus string
	}{
		{name: "completed work with error", record: common.RecordPartialResult, wantStatus: "partial"},
		{
			name:       "ordinary result with error",
			record:     func(value any) { common.RecordResult(value) },
			wantStatus: "error",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			common.BeginOutput([]string{"--json"})
			t.Cleanup(func() { common.BeginOutput(nil) })
			tc.record(struct {
				VMIDs []string `json:"vm_ids"`
			}{VMIDs: []string{"restored-vm"}})
			operationErr := &errs.DomainError{
				Code: errs.CodeSnapshotRestoreFailed, Class: errs.ClassInternal,
				Message: "restore failed after one VM", Op: "snapshot",
			}

			var output bytes.Buffer
			err := common.FinishOutput(&output, operationErr)
			require.ErrorIs(t, err, operationErr)
			var got common.OutputEnvelope
			require.NoError(t, json.Unmarshal(output.Bytes(), &got))
			want := common.OutputEnvelope{
				SchemaVersion: 1,
				Status:        tc.wantStatus,
				Data:          map[string]any{"vm_ids": []any{"restored-vm"}},
				Error: &common.OutputError{
					Code:    errs.CodeSnapshotRestoreFailed,
					Class:   errs.ClassInternal,
					Message: "restore failed after one VM",
					Op:      "snapshot",
				},
			}
			if diff := cmp.Diff(want, got); diff != "" {
				t.Errorf("structured output mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestRecordListResultKeepsArrayShape(t *testing.T) {
	tests := []struct {
		name  string
		items []string
		want  []any
	}{
		{name: "nil list", want: []any{}},
		{name: "populated list", items: []string{"alpha"}, want: []any{"alpha"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			common.BeginOutput([]string{"--json"})
			t.Cleanup(func() { common.BeginOutput(nil) })
			require.True(t, common.RecordListResult(tc.items))
			var output bytes.Buffer
			require.NoError(t, common.FinishOutput(&output, nil))
			var got common.OutputEnvelope
			require.NoError(t, json.Unmarshal(output.Bytes(), &got))
			want := common.OutputEnvelope{
				SchemaVersion: 1, Status: "success", Data: tc.want,
			}
			if diff := cmp.Diff(want, got); diff != "" {
				t.Errorf("list output mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
