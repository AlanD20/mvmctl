package inputs_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

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
