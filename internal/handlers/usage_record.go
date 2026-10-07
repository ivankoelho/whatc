package handlers

import (
	"context"
	"fmt"

	"github.com/google/uuid"
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
func (a *App) markUsageSendFailed(ctx context.Context, messageID uuid.UUID) {
	a.failOpen("send_failed", func() error { return a.Usage.MarkSendFailed(ctx, nil, messageID) })
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
