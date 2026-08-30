package loopmount

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseRootfsOSReleaseUsesStrictBoundedID(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		content string
		want    string
		wantErr string
	}{
		"missing ID uses specified Linux default": {
			content: "NAME=Linux\n", want: "linux",
		},
		"unquoted ID": {
			content: "NAME=Alpine\nID=alpine\nVERSION_ID=3.22\n", want: "alpine",
		},
		"quoted ID": {
			content: "ID=\"ubuntu\"\nPRETTY_NAME=\"Ubuntu 26.04\"\n", want: "ubuntu",
		},
		"comments and blank lines": {
			content: "# vendor data\n\nID='debian'\n", want: "debian",
		},
		"duplicate key": {
			content: "ID=alpine\nID=debian\n", wantErr: "duplicated",
		},
		"invalid assignment": {
			content: "ID alpine\n", wantErr: "assignment",
		},
		"invalid key": {
			content: "id=alpine\n", wantErr: "key",
		},
		"unterminated quote": {
			content: "ID=\"alpine\n", wantErr: "quoted value",
		},
		"trailing quoted content": {
			content: "ID=\"alpine\"ignored\n", wantErr: "quoted value",
		},
		"unescaped unquoted special character": {
			content: "ID=alpine$bad\n", wantErr: "unquoted value",
		},
		"invalid OS identifier": {
			content: "ID=Alpine\n", wantErr: "OS identifier",
		},
		"NUL byte": {
			content: "ID=alpine\x00\n", wantErr: "control",
		},
		"carriage return": {
			content: "ID=alpine\r\n", wantErr: "control",
		},
		"oversized file": {
			content: strings.Repeat("A", maxRootfsOSReleaseBytes+1), wantErr: "byte bound",
		},
		"oversized line": {
			content: "NAME=\"" + strings.Repeat("a", maxRootfsOSReleaseLineBytes) + "\"\n",
			wantErr: "line exceeds",
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := parseRootfsOSRelease([]byte(test.content))
			if test.wantErr != "" {
				require.Error(t, err)
				assert.ErrorContains(t, err, test.wantErr)
				assert.Empty(t, got)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, test.want, got)
		})
	}
}
