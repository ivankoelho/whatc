package handlers

import (
	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"gorm.io/gorm"
)

// conversationScopedSources are the dashboard / widget data sources whose rows belong to a conversation (a contact).
// The value is the column that holds the contact id on that source's table: contacts is the conversation itself.
var conversationScopedSources = map[string]string{
	"messages":  "contact_id",
	"transfers": "contact_id",
	"sessions":  "contact_id",
	"contacts":  "id",
}

// visibleContactsSQL is the sub-select of the contacts viewerID may open. narrowed is false when nothing narrows
// (conversations:view_all); with narrowed and an empty sub there is nothing to show (no viewer: aggregates must
// never fail open).
//
// It does not restate the visibility rule: the visible contacts come from scopeVisibleConversations, the same SQL
// every listing uses, so a widget shows exactly the conversations the person could open. The only value interpolated
// into the text is a UUID rendered by GORM.
func (a *App) visibleContactsSQL(orgID, viewerID uuid.UUID) (sub string, narrowed bool) {
	if viewerID == uuid.Nil {
		return "", true
	}
	if a.HasPermission(viewerID, models.ResourceConversations, models.ActionViewAll, orgID) {
		return "", false
	}
	return a.DB.ToSQL(func(tx *gorm.DB) *gorm.DB {
		q := tx.Model(&models.Contact{}).Select("contacts.id").Where("contacts.organization_id = ?", orgID)
		return a.scopeVisibleConversations(q, viewerID, orgID).Find(&[]models.Contact{})
	}), true
}

// conversationScopeCond is the SQL condition (without a leading AND) that narrows a conversation-bound data source to
// what viewerID may see, or "" when nothing narrows (a source that is not conversation-bound, or conversations:view_all).
// alias is the table alias prefix of the query ("m.", "t."; "" when the table is unaliased).
func (a *App) conversationScopeCond(dataSource string, orgID, viewerID uuid.UUID, alias string) string {
	col, ok := conversationScopedSources[dataSource]
	if !ok {
		return ""
	}
	sub, narrowed := a.visibleContactsSQL(orgID, viewerID)
	switch {
	case !narrowed:
		return ""
	case sub == "":
		return "1 = 0"
	}
	return alias + col + " IN (" + sub + ")"
}

// conversationScopeSQL is conversationScopeCond as an " AND ..." fragment for the hand-written widget queries.
func (a *App) conversationScopeSQL(dataSource string, orgID, viewerID uuid.UUID, alias string) string {
	if cond := a.conversationScopeCond(dataSource, orgID, viewerID, alias); cond != "" {
		return " AND " + cond
	}
	return ""
}

// scopeToVisibleConversations applies conversationScopeCond to a GORM query over the data source's own table.
func (a *App) scopeToVisibleConversations(q *gorm.DB, dataSource string, orgID, viewerID uuid.UUID) *gorm.DB {
	if cond := a.conversationScopeCond(dataSource, orgID, viewerID, ""); cond != "" {
		return q.Where(cond)
	}
	return q
}

// scopeRecentMessages is scopeToVisibleConversations for the dashboard's "newest N messages": the same rows, written
// as contact_id = ANY (ARRAY(<visible contacts>)). The plain IN (sub-select) lets the planner join first and sort the
// whole message table for a person who sees few conversations (measured: 400-600 ms on 1M messages); the array form
// keeps it a filtered scan with a top-N sort (130-280 ms). An empty array matches nothing, so the semantics are those of IN.
func (a *App) scopeRecentMessages(q *gorm.DB, orgID, viewerID uuid.UUID) *gorm.DB {
	sub, narrowed := a.visibleContactsSQL(orgID, viewerID)
	switch {
	case !narrowed:
		return q
	case sub == "":
		return q.Where("1 = 0")
	}
	return q.Where("contact_id = ANY (ARRAY(" + sub + "))")
}
