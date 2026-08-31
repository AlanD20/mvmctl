package cli

import (
	"sort"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
)

func TestJailerServiceCommandExposesOnlyVMLifecycleActions(t *testing.T) {
	cmd := newJailerServiceCmd()
	actions := make([]string, 0, len(cmd.Commands()))
	for _, child := range cmd.Commands() {
		actions = append(actions, child.Name())
		for _, obsoleteFlag := range []string{"version", "arch", "sha256"} {
			assert.Nilf(t, child.Flags().Lookup(obsoleteFlag), "%s must not expose --%s", child.Name(), obsoleteFlag)
		}
	}
	sort.Strings(actions)
	want := []string{"cleanup", "expose-snapshot", "expose-volume", "launch", "remove-volume"}
	if diff := cmp.Diff(want, actions); diff != "" {
		t.Errorf("Jailer service actions mismatch (-want +got):\n%s", diff)
	}
}
