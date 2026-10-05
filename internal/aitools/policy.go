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
	DenyArgsTooLarge            = models.AIToolDenyArgsTooLarge
	DenyWriteDisabled           = models.AIToolDenyWriteDisabled
	DenyFeatureNotAllowed       = models.AIToolDenyFeatureNotAllowed
)

// PolicyInput is everything the decision depends on. It is plain data so the policy is a pure,
// testable function.
type PolicyInput struct {
	GlobalEnabled bool // ai_tools.enabled
	Known         bool // the tool exists in the catalog
	OrgEnabled    bool // an administrator enabled it for this organization
	Risk          Risk

	// FeatureAllowed is false when the tool is restricted to other features than the caller's
	// (e.g. a write tool that only exists for chatbot_reply). The zero value is false, so a caller
	// that forgets to say it gets a denial, not a permission.
	FeatureAllowed bool
	// WriteEnabled is ai_tools.write_enabled; only a write tool looks at it.
	WriteEnabled bool
	// ConfirmationAvailable says a customer-confirmation mechanism is wired (Fase 9D); only a write
	// tool looks at it.
	ConfirmationAvailable bool
}

// Verdict is the policy's answer. Reason is empty when Allowed.
type Verdict struct {
	Allowed bool
	Reason  string
	// NeedsConfirmation is set on an allowed write tool: it may only PROPOSE; nothing is carried
	// out until the customer confirms.
	NeedsConfirmation bool
}

// Authorize decides whether a call may run. Deny by default, in a fixed order: global switch,
// existence, the organization's opt-in, the feature, then the risk class. A read tool is allowed.
// A write tool is allowed only to PROPOSE (NeedsConfirmation), and only with ai_tools.write_enabled
// on and a confirmation mechanism wired; otherwise it is denied. Being allowed here is not a
// capability the AI "has"; it is a decision about one call.
func Authorize(in PolicyInput) Verdict {
	switch {
	case !in.GlobalEnabled:
		return Verdict{Reason: DenyGlobalOff}
	case !in.Known:
		return Verdict{Reason: DenyUnknownTool}
	case !in.OrgEnabled:
		return Verdict{Reason: DenyNotEnabled}
	case !in.FeatureAllowed:
		return Verdict{Reason: DenyFeatureNotAllowed}
	}
	switch in.Risk {
	case RiskRead:
		return Verdict{Allowed: true}
	case RiskWrite:
		if !in.WriteEnabled {
			return Verdict{Reason: DenyWriteDisabled}
		}
		if !in.ConfirmationAvailable {
			return Verdict{Reason: DenyConfirmationUnavailable}
		}
		return Verdict{Allowed: true, NeedsConfirmation: true}
	}
	return Verdict{Reason: DenyConfirmationUnavailable} // an unknown class of risk is never allowed
}
