package handlers

import (
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/pkg/xprocess"
	"gorm.io/gorm"
)

// TransitionContactStatusForTest exposes transitionContactStatus to the
// external handlers_test package.
func (a *App) TransitionContactStatusForTest(
	contact *models.Contact,
	to models.ContactStatus,
	from []models.ContactStatus,
	actorID *uuid.UUID,
) (bool, error) {
	return a.transitionContactStatus(contact, to, from, actorID,
		statusCause{ActorType: "agent", ActorUserID: actorID, Reason: statusReasonAPI})
}

// SenderNameForBroadcastForTest exposes senderNameForBroadcast to the external
// handlers_test package, so its result can be pinned against senderName() —
// the REST-side producer of the same sent_by_user_name field.
func (a *App) SenderNameForBroadcastForTest(msg *models.Message) string {
	return a.senderNameForBroadcast(msg)
}

// SenderNameForTest exposes senderName to the external handlers_test package.
func SenderNameForTest(m *models.Message) string {
	return senderName(m)
}

// CreateAgentInitiatedTransferForTest exposes openAgentInitiatedTransfer. An
// existing active transfer is not an error here (idempotent, like the original).
func (a *App) CreateAgentInitiatedTransferForTest(account *models.WhatsAppAccount, contact *models.Contact, agentID uuid.UUID) {
	_ = a.openAgentInitiatedTransfer(account, contact, agentID)
}

// EnsureAgentOwnsConversationForTest exposes ensureAgentOwnsConversation.
func (a *App) EnsureAgentOwnsConversationForTest(account *models.WhatsAppAccount, contact *models.Contact, userID uuid.UUID) error {
	return a.ensureAgentOwnsConversation(account, contact, userID, false)
}

// ClaimTransferForTest exposes claimTransfer on the app's DB.
func (a *App) ClaimTransferForTest(t *models.AgentTransfer, agentID uuid.UUID) (bool, error) {
	return a.claimTransfer(a.DB, t, agentID)
}

// ReleaseContactForTest exposes releaseContact.
func (a *App) ReleaseContactForTest(contact *models.Contact, actorID *uuid.UUID, reason string) error {
	return a.releaseContact(contact, actorID, reason)
}

// CanViewConversationForTest / CanInteractWithConversationForTest expose the
// authorization functions to the external test package.
func (a *App) CanViewConversationForTest(userID, orgID uuid.UUID, contact *models.Contact) bool {
	return a.canViewConversation(userID, orgID, contact)
}
func (a *App) CanInteractWithConversationForTest(userID, orgID uuid.UUID, contact *models.Contact) bool {
	return a.canInteractWithConversation(userID, orgID, contact)
}

// ScopeVisibleConversationsForTest exposes scopeVisibleConversations to the
// external handlers_test package.
func (a *App) ScopeVisibleConversationsForTest(q *gorm.DB, userID, orgID uuid.UUID) *gorm.DB {
	return a.scopeVisibleConversations(q, userID, orgID)
}

// CanViewTeamMemberForTest exposes canViewTeamMember to the external
// handlers_test package.
func (a *App) CanViewTeamMemberForTest(viewerID, ownerID uuid.UUID) bool {
	return a.canViewTeamMember(viewerID, ownerID)
}

// SetConnectedForTest replaces the reaper's connectivity source.
func (r *PresenceReaper) SetConnectedForTest(fn func(orgID, userID uuid.UUID) bool) {
	r.isConnected = fn
}

// ValidateStickyAgentForTest exposes validateStickyAgent.
func (a *App) ValidateStickyAgentForTest(agentID, orgID uuid.UUID) *uuid.UUID {
	return a.validateStickyAgent(agentID, orgID)
}

// ResolveAIAPIKeyForTest exposes resolveAIAPIKey.
func (a *App) ResolveAIAPIKeyForTest(settings *models.ChatbotSettings) (string, error) {
	return a.resolveAIAPIKey(settings)
}

// GenerateAIResponseForTest exposes generateAIResponse.
func (a *App) GenerateAIResponseForTest(settings *models.ChatbotSettings, session *models.ChatbotSession, userMessage string) (string, error) {
	return a.generateAIResponse(settings, session, userMessage, aiFeatureChatbotReply)
}

// GetChatbotSettingsCachedForTest exposes getChatbotSettingsCached.
func (a *App) GetChatbotSettingsCachedForTest(orgID uuid.UUID, account string) (*models.ChatbotSettings, error) {
	return a.getChatbotSettingsCached(orgID, account)
}

// DiscoverXProcessOrdersForTest exposes the discovery pass of one organization.
func (a *App) DiscoverXProcessOrdersForTest(client *xprocess.Client, apiKey string, orgID uuid.UUID) {
	a.discoverXProcessOrders(client, apiKey, orgID)
}

// SaleOnOrAfterOpeningForTest exposes the date comparison used by discovery and candidates.
func SaleOnOrAfterOpeningForTest(sale, opened time.Time) bool {
	return saleOnOrAfterOpening(sale, opened)
}

// CreateDiscoveredXProcessLinkForTest links the order (codEmpresa, numPedido) to the
// opportunity exactly the way discovery does (one transaction: link, pointer, event).
func (a *App) CreateDiscoveredXProcessLinkForTest(orgID, oppID uuid.UUID, codEmpresa, numPedido, documento string) bool {
	m := &xprocessMatch{
		opp:    &models.SalesOpportunity{BaseModel: models.BaseModel{ID: oppID}},
		order:  xprocess.PedidoResumo{CodEmpresa: codEmpresa, NumPedido: numPedido},
		reason: "test",
	}
	_, ok := a.createDiscoveredXProcessLink(orgID, m, documento)
	return ok
}

// GenerateAIResponseNodeForTest exposes generateAIResponse as the flow's ai_response node calls it.
func (a *App) GenerateAIResponseNodeForTest(settings *models.ChatbotSettings, session *models.ChatbotSession, userMessage string) (string, error) {
	return a.generateAIResponse(settings, session, userMessage, aiFeatureChatbotNode)
}

// KnowledgeRAGEnabledForTest exposes knowledgeRAGEnabled.
func (a *App) KnowledgeRAGEnabledForTest(orgID uuid.UUID) bool {
	return a.knowledgeRAGEnabled(orgID)
}

// GenerateAIReplyForTest exposes generateAIReply (chatbot_reply): the text and, when the AI proposed
// an action, the server's confirmation buttons.
func (a *App) GenerateAIReplyForTest(settings *models.ChatbotSettings, session *models.ChatbotSession, userMessage string) (string, []map[string]any, error) {
	r, err := a.generateAIReply(settings, session, userMessage, aiFeatureChatbotReply)
	return r.Text, r.Buttons, err
}

// HandleAIToolTapForTest exposes handleAIToolConfirmationTap.
func (a *App) HandleAIToolTapForTest(account *models.WhatsAppAccount, contact *models.Contact, buttonID, wamid string) {
	a.handleAIToolConfirmationTap(account, contact, buttonID, wamid)
}

// ReleaseContactInTxForTest runs the release writes inside a transaction that
// is rolled back when rollback is true (to prove no history event outlives a
// rolled-back status change).
func (a *App) ReleaseContactInTxForTest(contact *models.Contact, actorID *uuid.UUID, rollback bool) error {
	errRollback := errors.New("rollback requested")
	err := a.DB.Transaction(func(tx *gorm.DB) error {
		if _, err := a.releaseContactTx(tx, contact, actorID, "test"); err != nil {
			return err
		}
		if rollback {
			return errRollback
		}
		return nil
	})
	if errors.Is(err, errRollback) {
		return nil
	}
	return err
}

// TransitionInTxWithBadCauseForTest changes the status inside a transaction with
// a history cause that cannot be stored (the event insert fails), then writes
// the contact's name in the SAME transaction. It proves the failing history
// insert neither aborts the transaction nor undoes the status change.
func (a *App) TransitionInTxWithBadCauseForTest(contact *models.Contact, to models.ContactStatus) error {
	return a.DB.Transaction(func(tx *gorm.DB) error {
		bad := statusCause{ActorType: "agent", Reason: strings.Repeat("x", 100)}
		if _, _, err := a.transitionContactStatusDB(tx, contact, to, nil, bad); err != nil {
			return err
		}
		return tx.Model(&models.Contact{}).Where("id = ?", contact.ID).Update("profile_name", "written-after").Error
	})
}

// CanAccessChatChannelForTest exposes canAccessChatChannel to handlers_test.
func (a *App) CanAccessChatChannelForTest(userID, orgID uuid.UUID, ch *models.ChatChannel) bool {
	return a.canAccessChatChannel(userID, orgID, ch)
}

// LoadChatChannelForTest exposes loadChatChannel to handlers_test.
func (a *App) LoadChatChannelForTest(orgID, channelID uuid.UUID) (*models.ChatChannel, bool) {
	return a.loadChatChannel(orgID, channelID)
}

// CanDeleteChatMessageForTest exposes canDeleteChatMessage to handlers_test.
func (a *App) CanDeleteChatMessageForTest(userID, orgID uuid.UUID, msg *models.ChatMessage, ch *models.ChatChannel) bool {
	return a.canDeleteChatMessage(userID, orgID, msg, ch)
}

// ReassignAwayTransferForTest exposes reassignAwayTransfer.
func (a *App) ReassignAwayTransferForTest(t *models.AgentTransfer, awayAgent uuid.UUID, event string) bool {
	return a.reassignAwayTransfer(t, awayAgent, event)
}

// SetChatAuditWriterForTest swaps the function that writes the audit entry of a chat
// change (to inject a failure) and returns the function that restores it.
func SetChatAuditWriterForTest(fn func(db *gorm.DB, orgID, userID uuid.UUID, userName, resourceType string, resourceID uuid.UUID, action models.AuditAction, changes ...map[string]any) error) func() {
	prev := chatAuditWriter
	chatAuditWriter = fn
	return func() { chatAuditWriter = prev }
}

// SetChatUploadsPerMinuteForTest lowers the chat upload rate limit and returns a restore func.
func SetChatUploadsPerMinuteForTest(n int) func() {
	old := chatUploadsPerMinute
	chatUploadsPerMinute = n
	return func() { chatUploadsPerMinute = old }
}

// CSV formula neutralization (csv_safe.go), exposed for the external test package.
func CSVSafeCellForTest(s string) string   { return csvSafeCell(s) }
func CSVUnsafeCellForTest(s string) string { return csvUnsafeCell(s) }

// CSVLooksLikeFormulaForTest: what a spreadsheet would evaluate (a trigger first, and not a plain number).
func CSVLooksLikeFormulaForTest(s string) bool {
	return startsWithFormulaTrigger(s) && !numericCell.MatchString(s)
}
