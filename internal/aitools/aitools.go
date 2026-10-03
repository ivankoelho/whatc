// Package aitools is the governance of the tools the chatbot's AI may call (Fase 9B): the catalog
// of tools that exist, the policy that decides whether a call is allowed, who is recorded as the
// actor, and the audit of every attempt. It sits between internal/ai (which only knows how to
// ask a provider for tool calls and run a bounded loop) and the application, so authorization and
// auditing never leak into the provider adapters.
//
// The catalog is EMPTY in production in 9B: no real tool exists yet.
package aitools

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/ai"
	"github.com/shridarpatil/whatomate/internal/models"
)

// Risk is the class of a tool. A write tool needs a human confirmation that does not exist yet
// (Fase 9D), so the policy denies every one of them until then.
type Risk string

const (
	RiskRead  Risk = models.AIToolRiskRead
	RiskWrite Risk = models.AIToolRiskWrite
)

// ActorKind says who acted. Only "ai" is used in 9B; the others are named so the model does not
// have to be reopened when a human confirms an action (9D). The customer who is talking to the
// chatbot is never an actor: they are the subject of the call.
type ActorKind string

const (
	ActorAI     ActorKind = models.AIActorKindAI
	ActorHuman  ActorKind = "human"
	ActorSystem ActorKind = "system"
)

// Actor is who is recorded as having made a tool call. Ref is the feature that asked, e.g.
// "chatbot_reply" or "chatbot_flow_node".
type Actor struct {
	Kind ActorKind
	Ref  string
}

// AIActor is the actor of every call made through the chatbot's AI.
func AIActor(feature string) Actor { return Actor{Kind: ActorAI, Ref: feature} }

// Scope is the ONLY context a tool receives. The server builds it from the chatbot session; it
// never comes from the arguments the model produced, and a tool cannot widen it. Everything a tool
// reads or writes must be filtered by OrganizationID.
type Scope struct {
	OrganizationID  uuid.UUID
	ContactID       *uuid.UUID
	SessionID       *uuid.UUID
	WhatsAppAccount string
}

// ToolSpec is one tool of the catalog. Specs live in code, not in the database: the database only
// records which of them an organization enabled.
type ToolSpec struct {
	Name        string
	Description string
	Parameters  json.RawMessage // a JSON Schema object (ai.ToolDefinition.Validate)
	Risk        Risk
	// Factory builds the real tool. It is called ONLY by the governed tool's Execute, after the
	// call was authorized and its "requested" audit row was written; never at resolution time.
	Factory func(Scope, Deps) ai.Tool
}

// Definition is the neutral definition offered to the provider.
func (s ToolSpec) Definition() ai.ToolDefinition {
	return ai.ToolDefinition{Name: s.Name, Description: s.Description, Parameters: s.Parameters}
}

// Catalog is the set of tools that exist. It is immutable once built.
type Catalog struct {
	specs map[string]ToolSpec
	names []string // sorted, so Definitions are stable
}

// NewCatalog validates the specs and builds the catalog.
func NewCatalog(specs ...ToolSpec) (*Catalog, error) {
	c := &Catalog{specs: make(map[string]ToolSpec, len(specs))}
	for _, s := range specs {
		if err := s.Definition().Validate(); err != nil {
			return nil, err
		}
		if s.Risk != RiskRead && s.Risk != RiskWrite {
			return nil, fmt.Errorf("aitools: tool %q has no valid risk class", s.Name)
		}
		if s.Factory == nil {
			return nil, fmt.Errorf("aitools: tool %q has no factory", s.Name)
		}
		if _, dup := c.specs[s.Name]; dup {
			return nil, fmt.Errorf("aitools: duplicate tool %q", s.Name)
		}
		c.specs[s.Name] = s
		c.names = append(c.names, s.Name)
	}
	sort.Strings(c.names)
	return c, nil
}

func (c *Catalog) Get(name string) (ToolSpec, bool) {
	if c == nil {
		return ToolSpec{}, false
	}
	s, ok := c.specs[name]
	return s, ok
}

// Names lists the tools of the catalog, sorted.
func (c *Catalog) Names() []string {
	if c == nil {
		return nil
	}
	return append([]string(nil), c.names...)
}

// DefaultCatalog is the production catalog. It is empty in 9B: the real tools arrive in 9C
// (read-only) and 9D (the first write, with human confirmation).
func DefaultCatalog() *Catalog {
	c, _ := NewCatalog()
	return c
}
