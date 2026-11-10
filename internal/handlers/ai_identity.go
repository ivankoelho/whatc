package handlers

import (
	"strings"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
)

// How an AI agent identifies itself. The customer sees a bold name before the text, in the same
// style a human attendant's signature uses; the internal history records who sent the message in
// Message.Metadata["sender"]. The AI is NEVER stored as a user: SentByUserID stays empty, so it does
// not appear as an employee in any list, portfolio or attendant metric.
const (
	defaultAIDisplayName = "Assistente Virtual"
	maxAIDisplayName     = 60

	// metadataSender is the key of Message.Metadata that records the automated sender.
	metadataSender = "sender"
	// senderTypeAIAgent is the value of Metadata["sender"]["type"] and of the API's sender_type.
	senderTypeAIAgent = "ai_agent"
)

// AISender is the identity an AI agent sends under.
type AISender struct {
	AgentID uuid.UUID
	Name    string
}

// cleanDisplayName makes a name safe to print inside WhatsApp's formatting: no line breaks and none of the
// characters that WhatsApp turns into bold, italic, strike-through or code, so a name can never break the
// signature or format the message that follows it.
func cleanDisplayName(s string) string {
	s = strings.Map(func(r rune) rune {
		switch r {
		case '*', '_', '~', '`', '\n', '\r', '\t':
			return -1
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if len([]rune(s)) > maxAIDisplayName {
		s = string([]rune(s)[:maxAIDisplayName])
	}
	return strings.TrimSpace(s)
}

// aiDisplayName is the name the agent shows: its own display name, or "Assistente Virtual".
func aiDisplayName(agent *models.AIAgent) string {
	if agent != nil {
		if n := cleanDisplayName(agent.DisplayName); n != "" {
			return n
		}
	}
	return defaultAIDisplayName
}

// newAISender returns the sender identity of agent (nil for no agent).
func newAISender(agent *models.AIAgent) *AISender {
	if agent == nil {
		return nil
	}
	return &AISender{AgentID: agent.ID, Name: aiDisplayName(agent)}
}

// prefix is what goes before the text on WhatsApp, in the style of a human signature ("*Name:*").
func (s *AISender) prefix() string { return "*" + s.Name + ":*\n" }

// metadata is the record stored with the message.
func (s *AISender) metadata() map[string]any {
	return map[string]any{
		"type":       senderTypeAIAgent,
		"agent_id":   s.AgentID.String(),
		"agent_name": s.Name,
	}
}

// messageSender reads the automated sender recorded on a stored message: its name and type, or empty
// strings for a human or unidentified message.
func messageSender(m *models.Message) (name, typ string) {
	var sender map[string]any
	switch v := m.Metadata[metadataSender].(type) {
	case map[string]any:
		sender = v
	case models.JSONB:
		sender = v
	default:
		return "", ""
	}
	typ, _ = sender["type"].(string)
	if typ != senderTypeAIAgent {
		return "", ""
	}
	name, _ = sender["agent_name"].(string)
	return name, typ
}
