package firewall

// ReadinessAction is the fixed root dispatcher action for the disposable firewall capability probe.
const ReadinessAction = "firewall-readiness"

// ReadinessRequest has no caller-selectable namespace, rule, address, or operation.
type ReadinessRequest struct{}

// ReadinessResult reports exact typed-generation application and readback in a disposable namespace.
type ReadinessResult struct {
	Ready bool `json:"ready"`
}

// Validate rejects a successful response which did not complete every capability checkpoint.
func (result ReadinessResult) Validate() error {
	if !result.Ready {
		return generationAuthorityError("firewall kernel readiness was not established", nil, nil)
	}
	return nil
}
