package network

// VMTAPName projects the deterministic guest TAP name for configuration rendering.
// It grants no authority to inspect, create, alter, or remove a network object.
func VMTAPName(ownerUID uint32, networkID, vmID string) (string, error) {
	key, err := newTopologyKey(ownerUID, vmID, networkID)
	if err != nil {
		return "", err
	}
	return string(deriveTopologyNames(key).tap), nil
}
