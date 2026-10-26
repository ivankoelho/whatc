package models

import (
	"bytes"
	"time"

	"github.com/google/uuid"
)

// Internal chat between users of the same organization (docs/superpowers/specs/
// 2026-10-16-chat-interno-nativo-design.md). It is a domain of its own: the
// author is a users row, the organization is the tenant, a team channel's
// members are the team's team_members. Nothing here duplicates conversation or
// contact data; context_* only references it.

// ChatChannelKind says how a channel's membership is decided.
type ChatChannelKind string

const (
	// ChatChannelKindTeam: one per team; membership IS team_members (no rows in
	// chat_channel_members).
	ChatChannelKindTeam ChatChannelKind = "team"
	// ChatChannelKindChannel: free channel, private; membership in chat_channel_members.
	ChatChannelKindChannel ChatChannelKind = "channel"
	// ChatChannelKindDM: exactly two users, identified by DMUserLowID/DMUserHighID.
	ChatChannelKindDM ChatChannelKind = "dm"
	// ChatChannelKindOccurrence: the internal discussion of ONE case (Occurrence).
	// It has no membership rows and no team: who may read and who may write is
	// decided by the case itself (authorizeOccurrence), so the discussion can never
	// be wider or narrower than the case. It never carries anything to a customer:
	// chat_messages is a different table from the WhatsApp messages and no code path
	// connects them.
	ChatChannelKindOccurrence ChatChannelKind = "occurrence"
)

// ChatChannel is a team channel, a free channel or a direct message.
//
// Uniqueness lives in partial unique indexes (see database coreIndexes): one
// team channel per (organization, team) and one DM per (organization, pair).
type ChatChannel struct {
	BaseModel
	OrganizationID uuid.UUID       `gorm:"type:uuid;index;not null" json:"organization_id"`
	Kind           ChatChannelKind `gorm:"size:20;not null" json:"kind"`
	TeamID         *uuid.UUID      `gorm:"type:uuid;index" json:"team_id,omitempty"`
	// OccurrenceID is set only for kind=occurrence (one channel per case).
	OccurrenceID *uuid.UUID `gorm:"type:uuid;index" json:"occurrence_id,omitempty"`
	Name         string     `gorm:"size:100" json:"name"`
	CreatedByID  *uuid.UUID `gorm:"type:uuid" json:"created_by_id,omitempty"`

	// ArchivedAt is set while the channel is archived (free channels only): hidden
	// from the normal list, closed to new messages, edits and members; history stays.
	ArchivedAt   *time.Time `gorm:"index" json:"archived_at,omitempty"`
	ArchivedByID *uuid.UUID `gorm:"type:uuid" json:"archived_by_id,omitempty"`

	// Canonical DM pair: low < high (see CanonicalDMPair). Only set for kind=dm.
	DMUserLowID  *uuid.UUID `gorm:"column:dm_user_low_id;type:uuid" json:"-"`
	DMUserHighID *uuid.UUID `gorm:"column:dm_user_high_id;type:uuid" json:"-"`
}

func (ChatChannel) TableName() string { return "chat_channels" }

// ChatChannelMember is membership of a free channel or a DM. Team channels
// never have rows here. Hard-deleted on removal (no soft delete), so the unique
// (channel_id, user_id) never blocks a later re-add.
type ChatChannelMember struct {
	ID             uuid.UUID `gorm:"type:uuid;primary_key;default:gen_random_uuid()" json:"id"`
	OrganizationID uuid.UUID `gorm:"type:uuid;index;not null" json:"organization_id"`
	ChannelID      uuid.UUID `gorm:"type:uuid;not null;uniqueIndex:idx_chat_member_channel_user" json:"channel_id"`
	UserID         uuid.UUID `gorm:"type:uuid;not null;uniqueIndex:idx_chat_member_channel_user;index" json:"user_id"`
	Role           string    `gorm:"size:20;not null;default:'member'" json:"role"` // owner, member
	JoinedAt       time.Time `gorm:"autoCreateTime" json:"joined_at"`
}

func (ChatChannelMember) TableName() string { return "chat_channel_members" }

// ChatMessageKind separates what people write from what the system records about the channel.
type ChatMessageKind string

const (
	ChatMessageKindUser   ChatMessageKind = "user"
	ChatMessageKindSystem ChatMessageKind = "system"
)

// System message events (ChatMessage.Meta["event"]). The UI translates them.
const (
	ChatEventMemberAdded   = "member_added"
	ChatEventMemberRemoved = "member_removed"
	ChatEventMemberLeft    = "member_left"
	ChatEventOwnerChanged  = "owner_changed"
	ChatEventRenamed       = "renamed"
	ChatEventArchived      = "archived"
	ChatEventReopened      = "reopened"
	ChatEventAdminJoined   = "admin_joined"
)

// ChatMessage is a message in a channel. ParentID is reserved for threads;
// ContextType/ContextID only reference a Whatc entity (never copy it).
type ChatMessage struct {
	BaseModel
	OrganizationID uuid.UUID  `gorm:"type:uuid;index;not null" json:"organization_id"`
	ChannelID      uuid.UUID  `gorm:"type:uuid;not null" json:"channel_id"`
	UserID         uuid.UUID  `gorm:"type:uuid;index;not null" json:"user_id"`
	Body           string     `gorm:"type:text;not null" json:"body"`
	ParentID       *uuid.UUID `gorm:"type:uuid;index" json:"parent_id,omitempty"`
	ContextType    string     `gorm:"size:30" json:"context_type,omitempty"`
	ContextID      *uuid.UUID `gorm:"type:uuid" json:"context_id,omitempty"`
	EditedAt       *time.Time `json:"edited_at,omitempty"`

	// Kind is "user" (written by a person) or "system" (a record about the channel,
	// e.g. a member added). A system message's Meta holds the event and its
	// parameters; it is never editable and never deletable.
	Kind ChatMessageKind `gorm:"size:10;not null;default:'user'" json:"kind"`
	Meta JSONB           `gorm:"type:jsonb" json:"meta,omitempty"`
}

func (ChatMessage) TableName() string { return "chat_messages" }

// ChatMessageReaction is one person's emoji on one message. Code is a key of the
// fixed palette (thumbs_up, heart, ...), never free text. Removing a reaction
// deletes the row; a deleted message just stops being read with its reactions.
type ChatMessageReaction struct {
	ID             uuid.UUID `gorm:"type:uuid;primaryKey" json:"id"`
	OrganizationID uuid.UUID `gorm:"type:uuid;index;not null" json:"organization_id"`
	ChannelID      uuid.UUID `gorm:"type:uuid;not null" json:"channel_id"`
	MessageID      uuid.UUID `gorm:"type:uuid;not null;uniqueIndex:idx_chat_reaction_unique,priority:1" json:"message_id"`
	UserID         uuid.UUID `gorm:"type:uuid;not null;uniqueIndex:idx_chat_reaction_unique,priority:2" json:"user_id"`
	Code           string    `gorm:"size:20;not null;uniqueIndex:idx_chat_reaction_unique,priority:3" json:"code"`
	CreatedAt      time.Time `json:"created_at"`
}

func (ChatMessageReaction) TableName() string { return "chat_message_reactions" }

// ChatRead is one user's read marker in one channel. It is separate from
// membership because team-channel members have no membership row. The marker is
// the (created_at, id) of the last read message, the same cursor shape the
// WhatsApp message list uses.
type ChatRead struct {
	ID                uuid.UUID  `gorm:"type:uuid;primary_key;default:gen_random_uuid()" json:"id"`
	OrganizationID    uuid.UUID  `gorm:"type:uuid;index;not null" json:"organization_id"`
	ChannelID         uuid.UUID  `gorm:"type:uuid;not null;uniqueIndex:idx_chat_read_channel_user" json:"channel_id"`
	UserID            uuid.UUID  `gorm:"type:uuid;not null;uniqueIndex:idx_chat_read_channel_user" json:"user_id"`
	LastReadAt        time.Time  `gorm:"not null" json:"last_read_at"`
	LastReadMessageID *uuid.UUID `gorm:"type:uuid" json:"last_read_message_id,omitempty"`
}

func (ChatRead) TableName() string { return "chat_reads" }

// CanonicalDMPair orders two user IDs (byte-wise) so a DM between A and B has
// one identity regardless of who opens it. The unique index on the pair then
// makes a concurrent double-create impossible.
func CanonicalDMPair(a, b uuid.UUID) (low, high uuid.UUID) {
	if bytes.Compare(a[:], b[:]) <= 0 {
		return a, b
	}
	return b, a
}

// ChatAttachment is a file (or voice recording) sent in a chat. It is uploaded
// first, pending (MessageID nil, visible only to the uploader), and fixed to a
// message in the same transaction that creates the message. StoragePath is
// server-generated and never leaves the server; Name is only a display label.
type ChatAttachment struct {
	ID             uuid.UUID  `gorm:"type:uuid;primaryKey" json:"id"`
	OrganizationID uuid.UUID  `gorm:"type:uuid;index;not null" json:"organization_id"`
	ChannelID      uuid.UUID  `gorm:"type:uuid;index;not null" json:"channel_id"`
	UploaderID     uuid.UUID  `gorm:"type:uuid;index;not null" json:"uploader_id"`
	MessageID      *uuid.UUID `gorm:"type:uuid;index" json:"message_id,omitempty"`
	Kind           string     `gorm:"size:10;not null" json:"kind"` // image | audio | file
	Name           string     `gorm:"size:255;not null" json:"name"`
	Mime           string     `gorm:"size:100;not null" json:"mime"`
	SizeBytes      int64      `gorm:"not null" json:"size"`
	SHA256         string     `gorm:"size:64;not null" json:"-"`
	StoragePath    string     `gorm:"size:500;not null" json:"-"`
	Width          *int       `json:"width,omitempty"`
	Height         *int       `json:"height,omitempty"`
	DurationMs     *int       `json:"duration_ms,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	PurgedAt       *time.Time `json:"-"`
}

func (ChatAttachment) TableName() string { return "chat_attachments" }

// ChatPollVote is one person's vote for one option of a poll message. The poll
// itself (question, options, single/multiple choice) lives in the message's Meta
// ("poll"), so a poll is an ordinary message that every existing path (access,
// archive, deletion, events) already governs. Removing a vote deletes the row.
type ChatPollVote struct {
	ID             uuid.UUID `gorm:"type:uuid;primaryKey" json:"id"`
	OrganizationID uuid.UUID `gorm:"type:uuid;index;not null" json:"organization_id"`
	ChannelID      uuid.UUID `gorm:"type:uuid;not null" json:"channel_id"`
	MessageID      uuid.UUID `gorm:"type:uuid;not null;uniqueIndex:idx_chat_poll_vote_unique,priority:1" json:"message_id"`
	UserID         uuid.UUID `gorm:"type:uuid;not null;uniqueIndex:idx_chat_poll_vote_unique,priority:2" json:"user_id"`
	OptionID       string    `gorm:"size:10;not null;uniqueIndex:idx_chat_poll_vote_unique,priority:3" json:"option_id"`
	CreatedAt      time.Time `json:"created_at"`
}

func (ChatPollVote) TableName() string { return "chat_poll_votes" }
