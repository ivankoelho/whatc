package aitools

import "github.com/shridarpatil/whatomate/internal/models"

// Deny reasons, as recorded in the audit. The model is never told which one applies.
const (
	DenyUnknownTool             = models.AIToolDenyUnknownTool
	DenyNotEnabled              = models.AIToolDenyNotEnabled
	DenyGlobalOff               = models.AIToolDenyGlobalOff
	DenyConfirmationUnavailable = models.AIToolDenyConfirmationUnavailable
	DenyLoopLimit               = models.AIToolDenyLoopLimit
	DenyAuditUnavailable        = models.AIToolDenyAuditUnavailable
)

// PolicyInput is everything the decision depends on. It is plain data so the policy is a pure,
// testable function.
type PolicyInput struct {
	GlobalEnabled bool // ai_tools.enabled
	Known         bool // the tool exists in the catalog
	OrgEnabled    bool // an administrator enabled it for this organization
	Risk          Risk
}

// Verdict is the policy's answer. Reason is empty when Allowed.
type Verdict struct {
	Allowed bool
	Reason  string
}

// Authorize decides whether a call may run. Deny by default, in a fixed order:
// global switch, then existence, then the organization's opt-in, then the risk class. A write
// tool is always denied here: it needs a human confirmation, which arrives in 9D. Being allowed
// here is not a capability the AI "has"; it is a decision about one call.
func Authorize(in PolicyInput) Verdict {
	switch {
	case !in.GlobalEnabled:
		return Verdict{Reason: DenyGlobalOff}
	case !in.Known:
		return Verdict{Reason: DenyUnknownTool}
	case !in.OrgEnabled:
		return Verdict{Reason: DenyNotEnabled}
	case in.Risk != RiskRead:
		return Verdict{Reason: DenyConfirmationUnavailable}
	}
	return Verdict{Allowed: true}
}
