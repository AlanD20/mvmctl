package loopmount

type rootfsProvisionTracker struct {
	modified       bool
	durable        bool
	outcomeUnknown bool
}

func (tracker *rootfsProvisionTracker) markModified() {
	tracker.modified = true
	tracker.durable = false
}

func (tracker *rootfsProvisionTracker) completeMutation() {
	tracker.modified = true
	tracker.outcomeUnknown = false
}

func (tracker *rootfsProvisionTracker) state() vmProvisionEffectState {
	if tracker.outcomeUnknown {
		return vmProvisionEffectStateOutcomeUnknown
	}
	if !tracker.modified {
		return vmProvisionEffectStateUnchanged
	}
	if !tracker.durable {
		return vmProvisionEffectStateModifiedDurabilityUncertain
	}
	return vmProvisionEffectStateModified
}
