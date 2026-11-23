package cli

import (
	"context"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/core/binary"
	"mvmctl/internal/lib/model"
	"mvmctl/internal/lib/system"
	"mvmctl/internal/testutil"
	"mvmctl/pkg/api"
)

func TestBinaryReleaseCompletionReturnsTypedSelectors(t *testing.T) {
	architecture := system.RuntimeArch()
	repo := testutil.NewBinaryRepo()
	for _, item := range []*model.BinaryItem{
		{ID: "fc-1161", Type: "firecracker", Architecture: architecture, Version: "1.16.1", IsPresent: true},
		{ID: "jl-1161", Type: "jailer", Architecture: architecture, Version: "1.16.1", IsPresent: true},
		{ID: "fc-1152", Type: "firecracker", Architecture: architecture, Version: "1.15.2", IsPresent: true},
		{ID: "jl-1152", Type: "jailer", Architecture: architecture, Version: "1.15.2", IsPresent: true},
	} {
		require.NoError(t, repo.Upsert(context.Background(), item))
	}
	op := &api.Operation{
		Repos:    api.Repos{Binary: repo},
		Services: api.Services{Binary: binary.NewService(repo, architecture)},
	}
	previous := opRef
	opRef = op
	t.Cleanup(func() { opRef = previous })
	binaryCmd := NewBinaryCmd(op, nil)

	for _, test := range []struct {
		name string
		want []string
	}{
		{name: "pull", want: []string{"firecracker", "firecracker:1.15.2", "firecracker:1.16.1"}},
		{name: "rm", want: []string{"firecracker:1.15.2", "firecracker:1.16.1"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			cmd, _, err := binaryCmd.Find([]string{test.name})
			require.NoError(t, err)
			got, directive := cmd.ValidArgsFunction(cmd, nil, "")
			assert.Equal(t, test.want, got)
			assert.Equal(t, uint(cobra.ShellCompDirectiveNoFileComp), uint(directive))
			assert.NotContains(t, got, "jailer")
			assert.NotContains(t, got, "fc-116")
		})
	}
}
