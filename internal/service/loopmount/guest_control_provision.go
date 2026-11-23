package loopmount

import (
	"context"

	"mvmctl/internal/infra"
	"mvmctl/internal/lib/guestcontrolauth"
)

const guestControlAgentMaterialPath = infra.GuestControlAgentMaterialPath

func writeGuestControlAgentMaterial(ctx context.Context, tree rootfsGuestTree, agent guestcontrolauth.AgentMaterial,
) (uint32, error) {
	if _, err := tree.ensureDirectory(ctx, rootfsAgentConfigDirectory, 0700, 0, 0); err != nil {
		return 0, err
	}
	raw, err := agent.Encode()
	if err != nil {
		return 0, rootfsAtomicError("encode protected Guest Control agent material", nil)
	}
	file := rootfsProvisionFile{path: guestControlAgentMaterialPath, content: raw, mode: 0600}
	changed, err := tree.replaceFile(ctx, file)
	if err != nil {
		return 0, err
	}
	if changed {
		return 1, nil
	}
	return 0, nil
}
