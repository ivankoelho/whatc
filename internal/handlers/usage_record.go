package handlers

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/internal/usage"
)

// Usage recording is instrumentation and runs FAIL-OPEN: nothing here may return
// an error, block, or panic into the send / receive / webhook path. A failure is
// logged and swallowed. With usage.record_enabled=false (or no Recorder) every
// helper is a no-op.

// failOpen runs fn and logs, never propagates, an error or a panic.
func (a *App) failOpen(what string, fn func() error) {
	if !a.Usage.Enabled() {
		return
	}
	defer func() {
		if r := recover(); r != nil {
			a.Log.Error("Usage recording panicked", "op", what, "panic", fmt.Sprint(r))
		}
	}()
	if err := fn(); err != nil {
		a.Log.Error("Usage recording failed", "op", what, "error", err)
	}
}

// recordMessageUsage writes the ledger row of a message just created.
func (a *App) recordMessageUsage(ctx context.Context, msg *models.Message, origin usage.Origin) {
	a.failOpen("record", func() error { return a.Usage.Record(ctx, nil, msg, origin) })
}

// attachUsageWamid links an accepted send's wamid to its ledger row and settles it.
func (a *App) attachUsageWamid(ctx context.Context, msg *models.Message, wamid string) {
	a.failOpen("attach_wamid", func() error {
		return a.Usage.AttachWamid(ctx, msg.OrganizationID, msg.ID, msg.WhatsAppAccount, wamid)
	})
}

// markUsageSendFailed records that the API refused the send.
func (a *App) markUsageSendFailed(ctx context.Context, msg *models.Message) {
	a.failOpen("send_failed", func() error { return a.Usage.MarkSendFailed(ctx, nil, msg.OrganizationID, msg.ID) })
}

// outgoingOrigin resolves who originated a send: the request's explicit Origin,
// else the one carried by ctx, else an agent when a user sent it.
func outgoingOrigin(ctx context.Context, req OutgoingMessageRequest, opts MessageSendOptions) usage.Origin {
	o := req.Origin
	if o.ActorType == "" {
		if c, ok := usage.OriginFrom(ctx); ok {
			o = c
		}
	}
	if o.ActorType == "" && opts.SentByUserID != nil {
		o.ActorType = usage.ActorAgent
	}
	if o.ActorType == usage.ActorAgent && o.ActorUserID == nil {
		o.ActorUserID = opts.SentByUserID
	}
	return o
}

// botCtx carries the origin of an automatic (chatbot) message into a send helper.
func botCtx(detail string) context.Context {
	return usage.WithOrigin(context.Background(), usage.Origin{ActorType: usage.ActorFlow, Detail: detail})
}

// aiCtx carries the origin of an AI-generated message into a send helper.
func aiCtx(detail string) context.Context {
	return usage.WithOrigin(context.Background(), usage.Origin{ActorType: usage.ActorAI, Detail: detail})
}

// nodeCtx carries the origin of a message sent by a flow-graph node: which flow
// and which node produced it.
func (a *App) nodeCtx(c *chatNodeCtx, node *ChatNode, actor usage.ActorType, detail string) context.Context {
	o := usage.Origin{ActorType: actor, FlowNodeID: node.ID, Detail: detail}
	if c != nil && c.session != nil && c.session.CurrentFlowID != nil {
		id := *c.session.CurrentFlowID
		o.FlowID = &id
	}
	return usage.WithOrigin(context.Background(), o)
}

// recordStatusUsage appends a Meta status event to the ledger and settles the
// message's usage row. It runs before the message's own status is updated and is
// fail-open: an error or panic is logged and the webhook carries on exactly as
// it would without it (the HTTP response never depends on it).
func (a *App) recordStatusUsage(phoneNumberID string, status WebhookStatus) {
	if !a.Usage.Enabled() {
		return
	}
	switch status.Status {
	case "sent", "delivered", "read", "failed":
	default:
		return
	}
	a.failOpen("status_event", func() error {
		account, err := a.getWhatsAppAccountCached(phoneNumberID)
		if err != nil {
			return fmt.Errorf("account for phone_number_id %s: %w", phoneNumberID, err)
		}
		ev := usage.StatusEvent{
			OrganizationID: account.OrganizationID, WhatsAppAccount: account.Name, Wamid: status.ID,
			Status: status.Status, EventAt: statusEventTime(status.Timestamp),
			RecipientCountry: usage.CountryOf(status.RecipientID),
		}
		if status.Pricing != nil {
			ev.Pricing = &usage.Pricing{Billable: status.Pricing.Billable, PricingModel: status.Pricing.PricingModel, Category: status.Pricing.Category}
		}
		if status.Conversation != nil {
			ev.ConversationID, ev.ConversationOrigin = status.Conversation.ID, status.Conversation.Origin.Type
		}
		if len(status.Errors) > 0 {
			ev.ErrorCode, ev.ErrorTitle = status.Errors[0].Code, status.Errors[0].Title
		}
		ctx := context.Background()
		inserted, err := a.Usage.RecordStatusEvent(ctx, ev)
		if err != nil || !inserted { // a duplicate event changes nothing and is not recomputed
			return err
		}
		return a.Usage.Settle(ctx, ev.OrganizationID, ev.WhatsAppAccount, ev.Wamid)
	})
}

// statusEventTime reads the unix-seconds timestamp of a status entry; an
// unreadable one falls back to now.
func statusEventTime(ts string) time.Time {
	if sec, err := strconv.ParseInt(ts, 10, 64); err == nil && sec > 0 {
		return time.Unix(sec, 0).UTC()
	}
	return time.Now().UTC()
}
