package vm

import "fmt"

// GenerateBatchNames generates VM names for batch creation.
// First VM keeps the base name; subsequent VMs get -N suffix.
func GenerateBatchNames(baseName string, count int) []string {
	if count == 1 {
		return []string{baseName}
	}
	names := make([]string, count)
	names[0] = baseName
	for i := 2; i <= count; i++ {
		names[i-1] = fmt.Sprintf("%s-%d", baseName, i)
	}
	return names
}
