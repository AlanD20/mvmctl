package cli_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"mvmctl/internal/cli"
)

func TestRequestsStructuredOutputUsesCommandFlagGrammar(t *testing.T) {
	for _, test := range []struct {
		name string
		args []string
		want bool
	}{
		{name: "global flag", args: []string{"--json", "vm", "ls"}, want: true},
		{name: "command flag", args: []string{"network", "floating-ip", "ls", "--json"}, want: true},
		{name: "explicit false", args: []string{"--json", "vm", "ls", "--json=false"}},
		{name: "string flag value", args: []string{"vm", "create", "test", "--image", "--json"}},
		{name: "guest command flag", args: []string{"exec", "test", "--", "tool", "--json"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			assert.Equal(t, test.want, cli.RequestsStructuredOutput(test.args))
		})
	}
}
