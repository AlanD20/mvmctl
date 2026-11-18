package env

import (
	"fmt"
	"mvmctl/internal/lib/workflow"
	"strings"
)

// resolveResourceReference only interprets explicit references; literal selectors survive unchanged.
func resolveResourceReference(state *workflow.SharedState, selector string) (string, error) {
	if !strings.HasPrefix(selector, "@") {
		return selector, nil
	}
	value, ok := state.Get(strings.TrimPrefix(selector, "@"))
	if !ok {
		return "", fmt.Errorf("reference %q has no completed output", selector)
	}
	var id string
	switch item := value.(type) {
	case *FloatingIPState:
		id = item.ResourceID
	case *VMState:
		id = item.VMID
	case *NetworkState:
		id = item.NetworkID
	case *KeyState:
		id = item.KeyID
	case *ImageState:
		id = item.ImageID
	case *ImageImportState:
		id = item.ImageID
	case *KernelState:
		id = item.KernelID
	case *BinaryState:
		id = item.BinaryID
	case *VolumeState:
		id = item.VolumeID
	default:
		return "", fmt.Errorf("reference %q is not a resource output", selector)
	}
	if id == "" {
		return "", fmt.Errorf("reference %q has an empty identity", selector)
	}
	return id, nil
}

func resolveCopyReference(state *workflow.SharedState, path string) (string, error) {
	if !strings.HasPrefix(path, "@vm:") {
		return path, nil
	}
	parts := strings.SplitN(path, ":", 3)
	if len(parts) != 3 || parts[2] == "" {
		return "", fmt.Errorf("copy reference requires @vm:step:/path")
	}
	id, err := resolveResourceReference(state, parts[0]+":"+parts[1])
	if err != nil {
		return "", err
	}
	return id + ":" + parts[2], nil
}
