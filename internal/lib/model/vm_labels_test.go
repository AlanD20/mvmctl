package model

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestVMLabelSelectorMatchesAllRequirements(t *testing.T) {
	selector := VMLabelSelector{"cluster": "demo", "role": "controller"}
	require.NoError(t, selector.Validate())
	assert.True(t, selector.Matches(VMLabels{"cluster": "demo", "role": "controller", "zone": "a"}))
	assert.False(t, selector.Matches(VMLabels{"cluster": "other", "role": "controller"}))
	assert.False(t, selector.Matches(VMLabels{"cluster": "demo"}))
	assert.Error(t, (VMLabelSelector{}).Validate())
	assert.False(t, (VMLabelSelector{}).Matches(VMLabels{"cluster": "demo"}))
}

func TestVMLabelsValidateBoundedEqualityValues(t *testing.T) {
	require.NoError(t, (VMLabels{}).Validate(), "replacing labels with an empty map clears membership")
	require.NoError(t, (VMLabels{strings.Repeat("a", 63): "node-1.us_east"}).Validate())

	for name, labels := range map[string]VMLabels{
		"wildcard": {"cluster": "*"},
		"negative": {"!cluster": "demo"},
		"empty":    {"cluster": ""},
		"unicode":  {"cluster": "démo"},
		"oversize": {"role": strings.Repeat("a", 64)},
	} {
		t.Run(name, func(t *testing.T) {
			require.Error(t, labels.Validate())
		})
	}
	labels := make(VMLabels)
	for i := range maxVMLabels {
		labels["key"+strconv.Itoa(i)] = "value"
	}
	require.NoError(t, labels.Validate())
	labels["one-more"] = "value"
	require.Error(t, labels.Validate())
}

func TestVMLabelDecodersRejectAmbiguousMaps(t *testing.T) {
	for _, format := range []struct {
		name      string
		decode    func([]byte, any) error
		valid     string
		empty     string
		duplicate string
		nonString string
	}{
		{"JSON", json.Unmarshal, `{"role":"worker"}`, `{}`, `{"role":"worker","role":"controller"}`, `{"role":1}`},
		{"YAML", yaml.Unmarshal, "role: worker", "{}", "role: worker\nrole: controller", "role: 1"},
	} {
		t.Run(format.name, func(t *testing.T) {
			var selector VMLabelSelector
			require.NoError(t, format.decode([]byte(format.valid), &selector))
			require.Error(t, format.decode([]byte(format.duplicate), &selector))
			require.Error(t, format.decode([]byte(format.nonString), &selector))
			require.Error(t, format.decode([]byte(format.empty), &selector))
			assert.True(t, selector.Matches(VMLabels{"role": "worker"}))
			assert.False(t, selector.Matches(VMLabels{"role": "controller"}))

			labels := VMLabels{"role": "worker"}
			require.NoError(t, format.decode([]byte(format.empty), &labels))
			assert.Empty(t, labels)
		})
	}
}
