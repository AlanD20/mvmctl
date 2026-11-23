package inputs_test

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"mvmctl/internal/lib/model"
	"mvmctl/pkg/api/inputs"
)

func TestFloatingIPDelegationLeaseHasNoDefaultAndRejectsUntimedArguments(t *testing.T) {
	for name, test := range map[string]struct {
		method, lease string
		want          time.Duration
		bad           bool
	}{
		"missing": {method: "associate", bad: true}, "fraction": {method: "renew", lease: "1500ms", bad: true},
		"negative": {method: "associate", lease: "-1s", bad: true}, "overflow": {method: "renew",
			lease: "9999999999999999h", bad: true},
		"untimed": {method: "inspect", lease: "0s", bad: true}, "unknown": {method: "any", bad: true},
		"explicit": {method: "associate", lease: "5s", want: 5 * time.Second}, "inspect": {method: "inspect"},
	} {
		t.Run(name, func(t *testing.T) {
			value, err := (inputs.FloatingIPDelegationGrantInput{Method: test.method, Lease: test.lease}).LeaseDuration()
			if test.bad {
				assert.Error(t, err)
				return
			}
			assert.NoError(t, err)
			assert.Equal(t, test.want, value)
		})
	}
}

func TestFloatingIPDelegationEnsureValidatesExpectedIncarnation(t *testing.T) {
	input := inputs.FloatingIPDelegationEnsureInput{
		FloatingIPDelegationGrantInput: inputs.FloatingIPDelegationGrantInput{
			Network: strings.Repeat("a", 64), Resource: strings.Repeat("b", 32), VM: strings.Repeat("c", 32),
			Method: "inspect",
		},
		ID: strings.Repeat("d", 32),
	}
	t.Run("reserved execution UID", func(t *testing.T) {
		input := input
		input.Expected = &model.FloatingIPGrantIncarnation{
			BootID: "11111111-1111-4111-8111-111111111111", LaunchGeneration: 1, ExecutionUID: ^uint32(0),
		}
		assert.ErrorContains(t, input.Validate(), "expected delegation launch identity is invalid")
	})
	t.Run("omitted guard", func(t *testing.T) {
		assert.NoError(t, input.Validate())
	})
}
