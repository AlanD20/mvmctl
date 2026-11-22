package firewall

import (
	"cmp"
	"encoding/json"
	"maps"
	"slices"
)

// The per-owner projection is prepared only on policy and label mutations.
// Source/user rows reference a destination bitmap over sorted immutable VM IDs.
type execPolicyProjection struct {
	vmIDs   []string
	rows    []execPolicyProjectionRow
	bitmaps [][]byte
}

type execPolicyProjectionKey struct {
	sourceVMID string
	targetUser string
}

type execPolicyProjectionRow struct {
	key    execPolicyProjectionKey
	bitmap int
}

// Budget materialization before publishing a root intent. The fixed row size
// accounts for encoded source/user keys and one reference; bitmaps are bounded
// before deduplication to cap peak working memory as well as artifact size.
const (
	execPolicyProjectionBudget   = 64 * 1024 * 1024
	execPolicyProjectionRowBytes = execPolicyIndexRowSize
)

func (intent Intent) projectExecPolicies(ownerUID uint32) (execPolicyProjection, error) {
	var projection execPolicyProjection
	for vmID, record := range intent.VMLabels {
		if record.OwnerUID == ownerUID && !record.Retired {
			projection.vmIDs = append(projection.vmIDs, vmID)
		}
	}
	slices.Sort(projection.vmIDs)
	fixedBytes := execPolicyIndexHeaderSize + len(projection.vmIDs)*execPolicyIndexVMIDSize
	if fixedBytes > execPolicyProjectionBudget {
		return execPolicyProjection{}, invalidGeneration("exec policy VM index exceeds its size budget")
	}
	slots := make(map[string]int, len(projection.vmIDs))
	for index, vmID := range projection.vmIDs {
		slots[vmID] = index
	}
	memberships := make(map[string][]int)
	members := func(endpoint VMPolicyEndpoint) ([]int, error) {
		if endpoint.VMID != "" {
			slot, found := slots[endpoint.VMID]
			if !found {
				return nil, invalidGeneration("exec projection requires the exact admitted VM")
			}
			return []int{slot}, nil
		}
		canonical, err := json.Marshal(endpoint.Labels)
		if err != nil {
			return nil, err
		}
		key := string(canonical)
		if matches, found := memberships[key]; found {
			return matches, nil
		}
		var matches []int
		for slot, vmID := range projection.vmIDs {
			if endpoint.Labels.Matches(intent.VMLabels[vmID].Labels) {
				matches = append(matches, slot)
			}
		}
		memberships[key] = matches
		return matches, nil
	}

	rows := make(map[execPolicyProjectionKey][]byte)
	width := (len(projection.vmIDs) + 7) / 8
	maxRows := (execPolicyProjectionBudget - fixedBytes) / (execPolicyProjectionRowBytes + width)
	for _, policy := range intent.ExecPolicies {
		if policy.OwnerUID != ownerUID {
			continue
		}
		sources, err := members(policy.Source)
		if err != nil {
			return execPolicyProjection{}, err
		}
		destinations, err := members(policy.Destination)
		if err != nil {
			return execPolicyProjection{}, err
		}
		if len(destinations) == 0 {
			continue
		}
		destBitmap := make([]byte, width)
		for _, destination := range destinations {
			destBitmap[destination/8] |= 1 << (destination % 8)
		}
		for _, source := range sources {
			key := execPolicyProjectionKey{projection.vmIDs[source], policy.TargetUser}
			bitmap, found := rows[key]
			if !found {
				if len(rows) >= maxRows {
					return execPolicyProjection{}, invalidGeneration("exec policy projection exceeds its size budget")
				}
				bitmap = make([]byte, width)
				rows[key] = bitmap
			}
			for index, value := range destBitmap {
				bitmap[index] |= value
			}
			// Equal source and destination IDs are never eligible for relay.
			bitmap[source/8] &^= 1 << (source % 8)
		}
	}

	keys := slices.SortedFunc(maps.Keys(rows), compareExecProjectionKeys)
	bitmaps := make(map[string]int)
	for _, key := range keys {
		bitmap := rows[key]
		index, found := bitmaps[string(bitmap)]
		if !found {
			index = len(projection.bitmaps)
			bitmaps[string(bitmap)] = index
			projection.bitmaps = append(projection.bitmaps, bitmap)
		}
		projection.rows = append(projection.rows, execPolicyProjectionRow{key, index})
	}
	return projection, nil
}

func compareExecProjectionKeys(left, right execPolicyProjectionKey) int {
	if order := cmp.Compare(left.sourceVMID, right.sourceVMID); order != 0 {
		return order
	}
	return cmp.Compare(left.targetUser, right.targetUser)
}
