package system

import (
	"strconv"
	"strings"
)

// ParseProcStatusField parses a field from /proc/[pid]/status format.
// Returns the integer value of the first whitespace-delimited token after
// the field name, or -1 if the field is not found or cannot be parsed.
func ParseProcStatusField(data, field string) int {
	for line := range strings.SplitSeq(data, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, field) {
			parts := strings.Fields(line)
			if len(parts) >= 2 {
				val, err := strconv.Atoi(parts[1])
				if err == nil {
					return val
				}
			}
		}
	}
	return -1
}
