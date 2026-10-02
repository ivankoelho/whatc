package ai

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
)

// Tool calling, provider-neutral. Whatc declares ToolDefinitions, a provider answers with
// ToolCalls, the caller runs them and sends ToolResults back. The wire formats stay inside
// the adapters; nothing here knows a vendor.
//
// Scope (Fase 9A): the contract and the adapters only. Who may run a tool, the catalog, the
// audit and the human confirmation are later phases and live outside this package.

// RoleTool marks a message that carries the ToolResults of the assistant's previous ToolCalls.
const RoleTool Role = "tool"

// ToolDefinition is a tool Whatc offers the model. Parameters is a JSON Schema object.
type ToolDefinition struct {
	Name        string // ^[a-zA-Z0-9_-]{1,64}$, the intersection of what the providers accept
	Description string
	Parameters  json.RawMessage // {"type":"object",...}
}

// ToolCall is the model asking for a tool. Arguments is always a JSON object.
type ToolCall struct {
	ID        string
	Name      string
	Arguments json.RawMessage
	// Opaque is transport state of the adapter that produced the call (e.g. a Gemini
	// thoughtSignature that must go back on the matching part). It is never persisted,
	// shown, audited or interpreted outside the adapter: callers only copy the ToolCall back.
	Opaque json.RawMessage
}

// ToolResult answers one ToolCall (matched by CallID). A tool failure is data for the model
// (IsError), not a Go error.
type ToolResult struct {
	CallID  string
	Name    string
	Content string
	IsError bool
}

// ToolChoice is deliberately limited to auto/none: forcing a tool is not needed by the current
// scope and will be reconsidered when a real tool requires it. "" means auto.
type ToolChoice string

const (
	ToolChoiceAuto ToolChoice = "auto"
	ToolChoiceNone ToolChoice = "none"
)

// FinishReason is why the provider stopped.
type FinishReason string

const (
	FinishStop      FinishReason = "stop"
	FinishToolCalls FinishReason = "tool_calls"
	FinishLength    FinishReason = "length"
	FinishOther     FinishReason = "other"
)

var toolNameRE = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

// ErrInvalidTool is returned (wrapped) for a ToolDefinition or conversation the contract rejects.
var ErrInvalidTool = errors.New("invalid tool")

// Validate checks the definition against the neutral contract.
func (d ToolDefinition) Validate() error {
	if !toolNameRE.MatchString(d.Name) {
		return fmt.Errorf("%w: name %q", ErrInvalidTool, d.Name)
	}
	var schema struct {
		Type string `json:"type"`
	}
	if len(d.Parameters) == 0 || json.Unmarshal(d.Parameters, &schema) != nil || schema.Type != "object" {
		return fmt.Errorf("%w: %q parameters must be a JSON Schema object", ErrInvalidTool, d.Name)
	}
	return nil
}

func validateTools(tools []ToolDefinition) *Error {
	seen := map[string]bool{}
	for _, t := range tools {
		if err := t.Validate(); err != nil {
			return &Error{Kind: KindInvalidRequest, Message: err.Error()}
		}
		if seen[t.Name] {
			return &Error{Kind: KindInvalidRequest, Message: fmt.Sprintf("duplicate tool %q", t.Name)}
		}
		seen[t.Name] = true
	}
	return nil
}

// argsObject parses a provider's arguments as a JSON object, tolerating the empty string some
// providers send for a no-argument call. Anything else is a malformed call.
func argsObject(raw string) (json.RawMessage, bool) {
	if raw == "" {
		return json.RawMessage(`{}`), true
	}
	var m map[string]json.RawMessage
	if json.Unmarshal([]byte(raw), &m) != nil || m == nil {
		return nil, false
	}
	return json.RawMessage(raw), true
}

func invalidToolCall(provider, msg string) *Error {
	return &Error{Provider: provider, Kind: KindInvalidToolCall, Message: msg}
}
