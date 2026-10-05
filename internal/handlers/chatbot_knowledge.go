package handlers

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/knowledge"
	"github.com/shridarpatil/whatomate/internal/models"
)

// knowledgeTimeout bounds ONLY the Knowledge lookup. When it expires the lookup is dropped
// and the AI call goes on without Knowledge: it never delays, retries or fails the reply.
const knowledgeTimeout = 2 * time.Second

// knowledgeRAGEnabled is the effective switch of Knowledge in the chatbot:
//
//	knowledge.rag_enabled (global config)  AND  ChatbotSettings.knowledge_enabled (the
//	organization's DEFAULT row, whats_app_account = '')
//
// With the global switch off nothing else is read (no Redis, no database). A missing
// settings row, or any error reading it, is false: Knowledge is opt-in.
func (a *App) knowledgeRAGEnabled(orgID uuid.UUID) bool {
	if a.Config == nil || !a.Config.Knowledge.RAGEnabled {
		return false
	}
	s, err := a.getChatbotSettingsCached(orgID, "")
	return err == nil && s != nil && s.KnowledgeEnabled
}

func (a *App) knowledgeRetriever() knowledge.Retriever {
	if a.KnowledgeRetriever != nil {
		return a.KnowledgeRetriever
	}
	return knowledge.LexicalRetriever{DB: a.DB}
}

// buildKnowledgeBlock returns the Knowledge section for the AI prompt of one customer
// message, or the empty Block. It NEVER fails: a retrieval error, a timeout or a panic is
// logged (ids only, no content) and answered with the empty Block, so the AI call goes on
// exactly as it would without Knowledge.
//
// The scope context is the session's CONTACT (its unit and department) and nothing else:
// not the assigned agent, not the team, not the WhatsApp account. A contact with no unit
// or department, or a contact that cannot be read, gets only the global content.
func (a *App) buildKnowledgeBlock(settings *models.ChatbotSettings, session *models.ChatbotSession, userMessage string) (block knowledge.Block) {
	defer func() {
		if r := recover(); r != nil {
			a.Log.Warn("Knowledge lookup panicked, continuing without it", "org_id", settings.OrganizationID, "panic", r)
			block = knowledge.Block{}
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), knowledgeTimeout)
	defer cancel()

	q := knowledge.Query{OrgID: settings.OrganizationID, Strategy: knowledge.Relaxed}
	previous := ""
	if session != nil {
		var c models.Contact
		err := a.DB.WithContext(ctx).Select("id", "unit_id", "department_id").
			Where("id = ? AND organization_id = ?", session.ContactID, settings.OrganizationID).First(&c).Error
		if err == nil {
			q.UnitID, q.DepartmentID = c.UnitID, c.DepartmentID
		}
		previous = a.previousCustomerMessage(ctx, session.ID, userMessage)
	}
	q.Text = knowledge.ComposeQuery(userMessage, previous)

	block, err := knowledge.Assemble(ctx, a.knowledgeRetriever(), q)
	if err != nil {
		a.Log.Warn("Knowledge lookup failed, continuing without it", "org_id", settings.OrganizationID, "error", err)
		return knowledge.Block{}
	}
	return block
}

// previousCustomerMessage is the latest earlier CUSTOMER message of the session with text
// (never an agent's or the AI's), skipping the current one if it is already stored.
func (a *App) previousCustomerMessage(ctx context.Context, sessionID uuid.UUID, current string) string {
	var msgs []models.ChatbotSessionMessage
	err := a.DB.WithContext(ctx).Where("session_id = ? AND direction = ?", sessionID, models.DirectionIncoming).
		Order("created_at DESC").Limit(6).Find(&msgs).Error
	if err != nil {
		return ""
	}
	current = strings.TrimSpace(current)
	for _, m := range msgs {
		if text := strings.TrimSpace(m.Message); text != "" && text != current {
			return text
		}
	}
	return ""
}
