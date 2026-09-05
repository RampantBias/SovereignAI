package controllermeta

import "k8s.io/apimachinery/pkg/types"

// ApprovalDecisionName gives each request one immutable decision slot.
func ApprovalDecisionName(requestUID types.UID) string {
	return "approval-" + string(requestUID)
}
