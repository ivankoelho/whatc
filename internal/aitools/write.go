package aitools

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/ai"
	"github.com/shridarpatil/whatomate/internal/models"
)

// A write tool never carries out its action when the model calls it. The model's call only PROPOSES
// (WriteTool.Propose); the action is carried out later, once, by the server, after the customer
// confirmed that exact proposal (WriteTool.Execute). Nothing here knows about WhatsApp or about
// transfers in particular beyond the TransferService port the first write tool needs.

// Proposal is what a write tool wants confirmed.
type Proposal struct {
	// Args is the canonical, sanitized payload that will be executed if the customer confirms. It is
	// an integrity payload, stored with the confirmation and bound to it by a digest; it is never
	// returned by any API.
	Args models.JSONB
}

// WriteTool is the implementation of a write tool, built by ToolSpec.WriteFactory.
type WriteTool interface {
	// Propose validates the model's arguments and the current state. It may not change anything.
	// With a nil Proposal nothing is asked of the customer and the ToolResult is what the model
	// hears. With a Proposal, the ToolResult must say that the action was NOT performed.
	Propose(ctx context.Context, call ai.ToolCall) (*Proposal, ai.ToolResult, error)
	// Execute carries out a confirmed proposal. It must be idempotent: the reconciler may call it
	// again for the same confirmation after a crash.
	Execute(ctx context.Context, args models.JSONB, confirmedAt time.Time) (ExecResult, error)
}

// ExecResult is what became of a confirmed action.
type ExecResult struct {
	Outcome    string     // models.AIConfirmationOutcome*
	TransferID *uuid.UUID // set when a transfer was created
}

// WriteDeps are the dependencies of a write tool. A write tool never gets a ReadDB.
type WriteDeps struct {
	Transfers TransferService
}

// TransferService is the port to the application's transfer logic, implemented in the handlers.
type TransferService interface {
	// HasActive says whether the contact already has an active transfer.
	HasActive(ctx context.Context, orgID, contactID uuid.UUID) bool
	// WithinBusinessHours says whether the organization accepts a transfer now.
	WithinBusinessHours(ctx context.Context, orgID uuid.UUID, account string) bool
	// TransferToQueue creates the transfer. It is idempotent and reports an outcome.
	TransferToQueue(ctx context.Context, scope Scope, notes string) (ExecResult, error)
}

// ConfirmSpec is everything the SERVER says to the customer about a proposal; nothing of it comes
// from the model.
type ConfirmSpec struct {
	// Prompt is the confirmation message; ttl is how long the customer has.
	Prompt func(ttl time.Duration) string
	// ConfirmLabel and DeclineLabel are the button titles (WhatsApp allows 20 characters).
	ConfirmLabel string
	DeclineLabel string
	DeclinedText string
	ExpiredText  string
	// OutcomeText is the message after a confirmed action: for models.AIConfirmationOutcomeCreated and
	// ...AlreadyActive. Outside business hours the organization's own out-of-hours message is used.
	OutcomeText func(outcome string) string
}

const maxButtonTitle = 20

func (c *ConfirmSpec) valid() bool {
	return c != nil && c.Prompt != nil && c.OutcomeText != nil && c.DeclinedText != "" && c.ExpiredText != "" &&
		c.ConfirmLabel != "" && c.DeclineLabel != "" &&
		len([]rune(c.ConfirmLabel)) <= maxButtonTitle && len([]rune(c.DeclineLabel)) <= maxButtonTitle
}

// featureAllowed says whether a tool exists for the feature (empty Features = every feature).
func (s ToolSpec) featureAllowed(feature string) bool {
	if len(s.Features) == 0 {
		return true
	}
	for _, f := range s.Features {
		if f == feature {
			return true
		}
	}
	return false
}
