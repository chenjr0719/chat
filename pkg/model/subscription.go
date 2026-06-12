package model

import (
	"errors"
	"time"
)

// ErrSubscriptionNotFound is returned when a subscription lookup finds no matching document.
var ErrSubscriptionNotFound = errors.New("subscription not found")

type Role string

const (
	RoleOwner Role = "owner"
	// RoleAdmin is recognized by message-gatekeeper's large-room bypass but not yet assignable via role-update RPC.
	RoleAdmin  Role = "admin"
	RoleMember Role = "member"
)

type SubscriptionUser struct {
	ID      string `json:"id" bson:"_id"`
	Account string `json:"account" bson:"account"`
	IsBot   bool   `json:"isBot" bson:"isBot"`
}

type Subscription struct {
	ID                 string           `json:"id" bson:"_id"`
	User               SubscriptionUser `json:"u" bson:"u"`
	RoomID             string           `json:"roomId" bson:"roomId"`
	SiteID             string           `json:"siteId" bson:"siteId"`
	Roles              []Role           `json:"roles" bson:"roles"`
	Name               string           `json:"name"                    bson:"name"`
	RoomType           RoomType         `json:"roomType"                bson:"roomType"`
	IsSubscribed       bool             `json:"isSubscribed,omitempty"  bson:"isSubscribed,omitempty"`
	HistorySharedSince *time.Time       `json:"historySharedSince,omitempty" bson:"historySharedSince,omitempty"`
	JoinedAt           time.Time        `json:"joinedAt" bson:"joinedAt"`
	LastSeenAt         *time.Time       `json:"lastSeenAt,omitempty" bson:"lastSeenAt,omitempty"`
	HasMention         bool             `json:"hasMention" bson:"hasMention"`
	ThreadUnread       []string         `json:"threadUnread,omitempty" bson:"threadUnread,omitempty"`
	Alert              bool             `json:"alert" bson:"alert"`
	Muted              bool             `json:"muted" bson:"muted"`
	Favorite           bool             `json:"favorite" bson:"favorite"`
	// Denormalized from Room.{Restricted,ExternalAccess}; the only place remote sites carry restricted state
	// (cross-site outbox mirrors subscriptions, not Room docs). Treat missing as false.
	Restricted     bool `json:"restricted,omitempty"     bson:"restricted,omitempty"`
	ExternalAccess bool `json:"externalAccess,omitempty" bson:"externalAccess,omitempty"`

	// Read-time baseline from the rooms $lookup/$addFields — internal only (json:"-"), surfaced to
	// clients via Room. Writers persisting a full Subscription doc MUST strip these four fields.
	UserCount        int        `json:"-" bson:"userCount,omitempty"`
	LastMsgAt        *time.Time `json:"-" bson:"lastMsgAt,omitempty"`
	LastMsgID        string     `json:"-" bson:"lastMsgId,omitempty"`
	LastMentionAllAt *time.Time `json:"-" bson:"lastMentionAllAt,omitempty"`

	// Room carries all room-derived fields, populated at read time from room-service's
	// RoomsInfoBatch RPC (baseline $lookup values when the RPC degrades). Never persisted.
	Room *SubscriptionRoom `json:"room,omitempty" bson:"-"`
}

// SubscriptionRoom is the room-derived view nested on an enriched subscription.
// Name is the room's canonical name — the subscription's own Name (counterpart
// account for DMs, app display name for botDMs) is never overwritten by it.
type SubscriptionRoom struct {
	SiteID           string     `json:"siteId,omitempty" bson:"-"`
	Name             string     `json:"name,omitempty" bson:"-"`
	UserCount        int        `json:"userCount,omitempty" bson:"-"`
	AppCount         int        `json:"appCount,omitempty" bson:"-"`
	LastMsgAt        *time.Time `json:"lastMsgAt,omitempty" bson:"-"`
	LastMsgID        string     `json:"lastMsgId,omitempty" bson:"-"`
	LastMentionAllAt *time.Time `json:"lastMentionAllAt,omitempty" bson:"-"`
	// Room E2E key delivered to authorized members for initial key bootstrap
	// on subscription.list (same payload as the room.key.get RPC).
	PrivateKey *string `json:"privateKey,omitempty" bson:"-"`
	KeyVersion *int    `json:"keyVersion,omitempty" bson:"-"`
}

// SubscriptionHRInfo carries the counterpart's HR-directory record on a DM subscription for sidebar/header rendering.
type SubscriptionHRInfo struct {
	Account string `json:"account" bson:"account"`
	Name    string `json:"name"    bson:"name"`
	EngName string `json:"engName" bson:"engName"`
}

// DMSubscription is the wire/storage shape for DM subscriptions: base Subscription plus counterpart HRInfo.
// The embedded pointer flattens at JSON marshal time; only emitted for RoomTypeDM — channels/botDMs ship plain Subscription.
type DMSubscription struct {
	*Subscription `bson:",inline"`
	HRInfo        *SubscriptionHRInfo `json:"hrInfo,omitempty" bson:"hrInfo,omitempty"`
}

// IsRoomMember reports whether sub represents an active membership; returns false for nil.
func IsRoomMember(sub *Subscription) bool {
	return sub != nil
}

// MessageThreadReadRequest is the body of the message.thread.read RPC.
type MessageThreadReadRequest struct {
	ThreadID string `json:"threadId"`
}
