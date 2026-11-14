package results

// EnvironmentPolicyReceipt identifies the exact grant atomically observed or created for a workflow.
type EnvironmentPolicyReceipt struct {
	PolicyID    string `json:"policy_id"`
	Incarnation string `json:"incarnation"`
	WasCreated  bool   `json:"was_created"`
}
