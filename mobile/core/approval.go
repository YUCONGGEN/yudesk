package core

import "encoding/json"

func (e *Engine) PendingApprovalJSON() string {
	b, _ := json.Marshal(e.approvals.Pending())
	return string(b)
}
func (e *Engine) ResolveApproval(id string, accept bool) error {
	return e.approvals.Resolve(id, accept)
}
