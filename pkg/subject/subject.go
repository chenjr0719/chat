package subject

import (
	"fmt"
	"strings"
)

// ParseUserRoomSubject extracts account and roomID from "chat.user.{account}.*.room.{roomID}.…".
func ParseUserRoomSubject(subj string) (account, roomID string, ok bool) {
	parts := strings.Split(subj, ".")
	if len(parts) < 5 || parts[0] != "chat" || parts[1] != "user" {
		return "", "", false
	}
	account = parts[2]
	if !isValidAccountToken(account) {
		return "", "", false
	}
	// Find "room" token after user position
	for i := 3; i < len(parts)-1; i++ {
		if parts[i] == "room" {
			roomID = parts[i+1]
			if !isValidAccountToken(roomID) {
				return "", "", false
			}
			return account, roomID, true
		}
	}
	return "", "", false
}

func ParseUserRoomSiteSubject(subj string) (account, roomID, siteID string, ok bool) {
	parts := strings.Split(subj, ".")
	if len(parts) < 7 || parts[0] != "chat" || parts[1] != "user" || parts[3] != "room" {
		return "", "", "", false
	}
	return parts[2], parts[4], parts[5], true
}

// --- Specific subject builders ---

func MsgSend(account, roomID, siteID string) string {
	return fmt.Sprintf("chat.user.%s.room.%s.%s.msg.send", account, roomID, siteID)
}

// MsgGet returns the concrete subject for a GetMessageByID request to history-service. Pair with MsgGetPattern.
func MsgGet(account, roomID, siteID string) string {
	return fmt.Sprintf("chat.user.%s.request.room.%s.%s.msg.get", account, roomID, siteID)
}

func UserResponse(account, requestID string) string {
	return fmt.Sprintf("chat.user.%s.response.%s", account, requestID)
}

func RoomMetadataUpdate(roomID string) string {
	return fmt.Sprintf("chat.room.%s.event.metadata.update", roomID)
}

func RoomMsgStream(roomID string) string {
	return fmt.Sprintf("chat.room.%s.stream.msg", roomID)
}

func UserRoomUpdate(account string) string {
	return fmt.Sprintf("chat.user.%s.event.room.update", account)
}

func UserMsgStream(account string) string {
	return fmt.Sprintf("chat.user.%s.stream.msg", account)
}

func MemberRoleUpdate(account, roomID, siteID string) string {
	return fmt.Sprintf("chat.user.%s.request.room.%s.%s.member.role-update", account, roomID, siteID)
}

// RoomRename is the request/reply subject for the rename RPC; caller must supply a server-derived account identity.
func RoomRename(account, roomID, siteID string) string {
	return fmt.Sprintf("chat.user.%s.request.room.%s.%s.room.rename", account, roomID, siteID)
}

// RoomRestricted is the server-to-server request/reply subject for the restricted RPC.
// Admin-only; not exposed to clients — admin tooling calls this directly.
func RoomRestricted(siteID string) string {
	return fmt.Sprintf("chat.server.request.room.%s.restricted", siteID)
}

func MemberRemove(account, roomID, siteID string) string {
	return fmt.Sprintf("chat.user.%s.request.room.%s.%s.member.remove", account, roomID, siteID)
}

func MemberList(account, roomID, siteID string) string {
	return fmt.Sprintf("chat.user.%s.request.room.%s.%s.member.list", account, roomID, siteID)
}

func MemberEvent(roomID string) string {
	return fmt.Sprintf("chat.room.%s.event.member", roomID)
}

func RoomCanonical(siteID, operation string) string {
	return fmt.Sprintf("chat.room.canonical.%s.%s", siteID, operation)
}

// RoomCanonicalMemberEvent returns the post-mutation member-event subject (mute-only today).
func RoomCanonicalMemberEvent(siteID, eventType string) string {
	return fmt.Sprintf("chat.room.canonical.%s.event.member.%s", siteID, eventType)
}

func SubscriptionUpdate(account string) string {
	return fmt.Sprintf("chat.user.%s.event.subscription.update", account)
}

func RoomMetadataChanged(account string) string {
	return fmt.Sprintf("chat.user.%s.event.room.metadata.update", account)
}

func Notification(account string) string {
	return fmt.Sprintf("chat.user.%s.notification", account)
}

func Outbox(siteID, destSiteID, eventType string) string {
	return fmt.Sprintf("outbox.%s.to.%s.%s", siteID, destSiteID, eventType)
}

// InboxMemberAdded is the local-publish subject for a same-site member_added event (no `aggregate` segment).
func InboxMemberAdded(siteID string) string {
	return fmt.Sprintf("chat.inbox.%s.member_added", siteID)
}

// InboxMemberRemoved is the local-publish subject for a same-site member_removed event (no `aggregate` segment).
func InboxMemberRemoved(siteID string) string {
	return fmt.Sprintf("chat.inbox.%s.member_removed", siteID)
}

// InboxMemberAddedAggregate is the transformed subject for a federated member_added after INBOX SubjectTransform.
func InboxMemberAddedAggregate(siteID string) string {
	return fmt.Sprintf("chat.inbox.%s.aggregate.member_added", siteID)
}

// InboxMemberRemovedAggregate is the transformed subject for a federated
// member_removed event.
func InboxMemberRemovedAggregate(siteID string) string {
	return fmt.Sprintf("chat.inbox.%s.aggregate.member_removed", siteID)
}

// InboxAggregateAll returns the wildcard `chat.inbox.{siteID}.aggregate.>` matching all federated events.
// Use with ConsumerConfig.FilterSubjects to scope to the federated lane only.
func InboxAggregateAll(siteID string) string {
	return fmt.Sprintf("chat.inbox.%s.aggregate.>", siteID)
}

// InboxMemberEventSubjects returns subject filters for both local and federated member events; use with FilterSubjects.
func InboxMemberEventSubjects(siteID string) []string {
	return []string{
		InboxMemberAdded(siteID),
		InboxMemberRemoved(siteID),
		InboxMemberAddedAggregate(siteID),
		InboxMemberRemovedAggregate(siteID),
	}
}

func MsgCanonicalCreated(siteID string) string {
	return fmt.Sprintf("chat.msg.canonical.%s.created", siteID)
}

func MsgCanonicalUpdated(siteID string) string {
	return fmt.Sprintf("chat.msg.canonical.%s.updated", siteID)
}

func MsgCanonicalDeleted(siteID string) string {
	return fmt.Sprintf("chat.msg.canonical.%s.deleted", siteID)
}

func MsgCanonicalPinned(siteID string) string {
	return fmt.Sprintf("chat.msg.canonical.%s.pinned", siteID)
}

func MsgCanonicalUnpinned(siteID string) string {
	return fmt.Sprintf("chat.msg.canonical.%s.unpinned", siteID)
}

func MsgCanonicalReacted(siteID string) string {
	return fmt.Sprintf("chat.msg.canonical.%s.reacted", siteID)
}

func RoomEvent(roomID string) string {
	return fmt.Sprintf("chat.room.%s.event", roomID)
}

func UserRoomEvent(account string) string {
	return fmt.Sprintf("chat.user.%s.event.room", account)
}

func RoomKeyUpdate(account string) string {
	return fmt.Sprintf("chat.user.%s.event.room.key", account)
}

// --- Room CRUD request builders ---

// RoomsInfoBatch is the server-to-server request subject for batch room info lookups.
func RoomsInfoBatch(siteID string) string {
	return fmt.Sprintf("chat.server.request.room.%s.info.batch", siteID)
}

// RoomKeyEnsure is the server-to-server request subject for the room key ensure RPC.
func RoomKeyEnsure(siteID string) string {
	return fmt.Sprintf("chat.server.request.room.%s.key.ensure", siteID)
}

// RoomKeyGet is the user-facing subject for the room key fetch RPC. Pair with RoomKeyGetWildcard.
func RoomKeyGet(account, roomID, siteID string) string {
	return fmt.Sprintf("chat.user.%s.request.room.%s.%s.key.get", account, roomID, siteID)
}

// RoomKeyGetWildcard is the subscription pattern room-service uses to
// receive RoomKeyGet requests from any account / roomID at its siteID.
func RoomKeyGetWildcard(siteID string) string {
	return fmt.Sprintf("chat.user.*.request.room.*.%s.key.get", siteID)
}

// RoomCreateDMSync is the server-to-server request subject for synchronous DM/botDM creation.
func RoomCreateDMSync(siteID string) string {
	return fmt.Sprintf("chat.server.request.room.%s.create.dm", siteID)
}

// --- Wildcard patterns for subscriptions ---

func MsgSendWildcard(siteID string) string {
	return fmt.Sprintf("chat.user.*.room.*.%s.msg.send", siteID)
}

func MemberRoleUpdateWildcard(siteID string) string {
	return fmt.Sprintf("chat.user.*.request.room.*.%s.member.role-update", siteID)
}

// RoomRenameWildcard is the queue-subscribe pattern on a site.
func RoomRenameWildcard(siteID string) string {
	return fmt.Sprintf("chat.user.*.request.room.*.%s.room.rename", siteID)
}

func MemberRemoveWildcard(siteID string) string {
	return fmt.Sprintf("chat.user.*.request.room.*.%s.member.remove", siteID)
}

func MemberListWildcard(siteID string) string {
	return fmt.Sprintf("chat.user.*.request.room.*.%s.member.list", siteID)
}

// MemberStatuses is the concrete subject for the per-room member.statuses RPC. Pair with MemberStatusesWildcard.
func MemberStatuses(account, roomID, siteID string) string {
	return fmt.Sprintf("chat.user.%s.request.room.%s.%s.member.statuses", account, roomID, siteID)
}

// MemberStatusesWildcard is the per-site subscription pattern for member.statuses.
func MemberStatusesWildcard(siteID string) string {
	return fmt.Sprintf("chat.user.*.request.room.*.%s.member.statuses", siteID)
}

// MentionableSubscriptions is the concrete subject for subscription.mentionable. Pair with the Wildcard variant.
func MentionableSubscriptions(account, roomID, siteID string) string {
	return fmt.Sprintf("chat.user.%s.request.room.%s.%s.subscription.mentionable", account, roomID, siteID)
}

// MentionableSubscriptionsWildcard is the per-site subscription pattern for subscription.mentionable.
func MentionableSubscriptionsWildcard(siteID string) string {
	return fmt.Sprintf("chat.user.*.request.room.*.%s.subscription.mentionable", siteID)
}

// OrgMembers builds the subject for listing org members; siteID selects the user directory (per-site).
// Panics on any token containing NATS wildcard characters.
func OrgMembers(account, orgID, siteID string) string {
	if !isValidAccountToken(account) || !isValidAccountToken(orgID) || !isValidAccountToken(siteID) {
		panic("invalid subject token: contains NATS wildcard characters")
	}
	return fmt.Sprintf("chat.user.%s.request.orgs.%s.%s.members", account, orgID, siteID)
}

// OrgMembersWildcard is the per-site subscription pattern for the
// list-org-members endpoint.
func OrgMembersWildcard(siteID string) string {
	return fmt.Sprintf("chat.user.*.request.orgs.*.%s.members", siteID)
}

// ParseOrgMembersSubject returns (orgID, siteID) from "chat.user.{account}.request.orgs.{orgId}.{siteId}.members".
// Returns ok=false when any token contains NATS wildcard characters.
func ParseOrgMembersSubject(subj string) (orgID, siteID string, ok bool) {
	parts := strings.Split(subj, ".")
	if len(parts) != 8 {
		return "", "", false
	}
	if parts[0] != "chat" || parts[1] != "user" || parts[3] != "request" ||
		parts[4] != "orgs" || parts[7] != "members" {
		return "", "", false
	}
	if !isValidAccountToken(parts[2]) || !isValidAccountToken(parts[5]) || !isValidAccountToken(parts[6]) {
		return "", "", false
	}
	return parts[5], parts[6], true
}

func RoomCanonicalWildcard(siteID string) string {
	return fmt.Sprintf("chat.room.canonical.%s.>", siteID)
}

func MsgHistoryWildcard(siteID string) string {
	return fmt.Sprintf("chat.user.*.request.room.*.%s.msg.history", siteID)
}

func MsgThreadWildcard(siteID string) string {
	return fmt.Sprintf("chat.user.*.request.room.*.%s.msg.thread", siteID)
}

func MsgCanonicalWildcard(siteID string) string {
	return fmt.Sprintf("chat.msg.canonical.%s.>", siteID)
}

func OutboxWildcard(siteID string) string {
	return fmt.Sprintf("outbox.%s.>", siteID)
}

// RoomsInfoBatchSubscribe is the per-site subscription subject for room-service.
func RoomsInfoBatchSubscribe(siteID string) string {
	return fmt.Sprintf("chat.server.request.room.%s.info.batch", siteID)
}

func UserResponseWildcard() string {
	return "chat.user.*.response.>"
}

func RoomEventWildcard() string {
	return "chat.room.*.event"
}

func UserRoomEventWildcard() string {
	return "chat.user.*.event.room"
}

// --- natsrouter patterns (use {param} placeholders for named extraction) ---

func MsgHistoryPattern(siteID string) string {
	return fmt.Sprintf("chat.user.{account}.request.room.{roomID}.%s.msg.history", siteID)
}

func MsgNextPattern(siteID string) string {
	return fmt.Sprintf("chat.user.{account}.request.room.{roomID}.%s.msg.next", siteID)
}

func MsgSurroundingPattern(siteID string) string {
	return fmt.Sprintf("chat.user.{account}.request.room.{roomID}.%s.msg.surrounding", siteID)
}

// MsgGetPattern is the natsrouter pattern for GetMessageByID. Pair with MsgGet for the concrete subject.
func MsgGetPattern(siteID string) string {
	return fmt.Sprintf("chat.user.{account}.request.room.{roomID}.%s.msg.get", siteID)
}

// MsgEditPattern is the natsrouter pattern for editing a message.
func MsgEditPattern(siteID string) string {
	return fmt.Sprintf("chat.user.{account}.request.room.{roomID}.%s.msg.edit", siteID)
}

// MsgDeletePattern is the natsrouter pattern for soft-deleting a message.
func MsgDeletePattern(siteID string) string {
	return fmt.Sprintf("chat.user.{account}.request.room.{roomID}.%s.msg.delete", siteID)
}

// MsgPinPattern is the natsrouter pattern for pinning a message.
func MsgPinPattern(siteID string) string {
	return fmt.Sprintf("chat.user.{account}.request.room.{roomID}.%s.msg.pin", siteID)
}

// MsgUnpinPattern is the natsrouter pattern for unpinning a message.
func MsgUnpinPattern(siteID string) string {
	return fmt.Sprintf("chat.user.{account}.request.room.{roomID}.%s.msg.unpin", siteID)
}

// MsgPinnedListPattern is the natsrouter pattern for listing a room's pinned messages.
func MsgPinnedListPattern(siteID string) string {
	return fmt.Sprintf("chat.user.{account}.request.room.{roomID}.%s.msg.pinned.list", siteID)
}

// MsgReactPattern is the natsrouter pattern for toggling a reaction on a message.
func MsgReactPattern(siteID string) string {
	return fmt.Sprintf("chat.user.{account}.request.room.{roomID}.%s.msg.react", siteID)
}

func MsgThreadPattern(siteID string) string {
	return fmt.Sprintf("chat.user.{account}.request.room.{roomID}.%s.msg.thread", siteID)
}

// MsgHistory is the concrete subject for LoadHistory. Pair with MsgHistoryPattern for server-side registration.
func MsgHistory(account, roomID, siteID string) string {
	if !isValidAccountToken(account) {
		panic("invalid account token: contains NATS wildcard characters")
	}
	return fmt.Sprintf("chat.user.%s.request.room.%s.%s.msg.history", account, roomID, siteID)
}

// MsgThread is the concrete subject for GetThreadMessages. Pair with MsgThreadPattern for server-side registration.
func MsgThread(account, roomID, siteID string) string {
	if !isValidAccountToken(account) {
		panic("invalid account token: contains NATS wildcard characters")
	}
	return fmt.Sprintf("chat.user.%s.request.room.%s.%s.msg.thread", account, roomID, siteID)
}

func MemberAdd(account, roomID, siteID string) string {
	return fmt.Sprintf("chat.user.%s.request.room.%s.%s.member.add", account, roomID, siteID)
}

func MemberAddWildcard(siteID string) string {
	return fmt.Sprintf("chat.user.*.request.room.*.%s.member.add", siteID)
}

// MessageRead returns the concrete subject for the per-user message-read RPC.
// Pair with MessageReadWildcard for room-service's QueueSubscribe.
func MessageRead(account, roomID, siteID string) string {
	return fmt.Sprintf("chat.user.%s.request.room.%s.%s.message.read", account, roomID, siteID)
}

// MessageReadWildcard is the per-site subscription pattern for the message-read RPC.
func MessageReadWildcard(siteID string) string {
	return fmt.Sprintf("chat.user.*.request.room.*.%s.message.read", siteID)
}

func MessageReadReceipt(account, roomID, siteID string) string {
	return fmt.Sprintf("chat.user.%s.request.room.%s.%s.message.read-receipt", account, roomID, siteID)
}

func MessageReadReceiptWildcard(siteID string) string {
	return fmt.Sprintf("chat.user.*.request.room.*.%s.message.read-receipt", siteID)
}

// MessageThreadRead returns the concrete subject for the per-user mark-thread-as-read RPC.
func MessageThreadRead(account, roomID, siteID string) string {
	return fmt.Sprintf("chat.user.%s.request.room.%s.%s.message.thread.read", account, roomID, siteID)
}

// MessageThreadReadWildcard is the per-site subscription pattern for the mark-thread-as-read RPC.
func MessageThreadReadWildcard(siteID string) string {
	return fmt.Sprintf("chat.user.*.request.room.*.%s.message.thread.read", siteID)
}

// MuteToggle returns the concrete subject for the per-user mute.toggle RPC.
func MuteToggle(account, roomID, siteID string) string {
	return fmt.Sprintf("chat.user.%s.request.room.%s.%s.mute.toggle", account, roomID, siteID)
}

// MuteToggleWildcard is the per-site subscription pattern for the mute.toggle RPC.
func MuteToggleWildcard(siteID string) string {
	return fmt.Sprintf("chat.user.*.request.room.*.%s.mute.toggle", siteID)
}

// FavoriteToggle returns the concrete subject for the per-user favorite.toggle RPC.
func FavoriteToggle(account, roomID, siteID string) string {
	return fmt.Sprintf("chat.user.%s.request.room.%s.%s.favorite.toggle", account, roomID, siteID)
}

// FavoriteToggleWildcard is the per-site subscription pattern for the favorite.toggle RPC.
func FavoriteToggleWildcard(siteID string) string {
	return fmt.Sprintf("chat.user.*.request.room.*.%s.favorite.toggle", siteID)
}

// RoomAppTabs returns the concrete subject for the GetRoomAppTabs RPC.
// Pair with RoomAppTabsWildcard for room-service's QueueSubscribe.
func RoomAppTabs(account, roomID, siteID string) string {
	if !isValidAccountToken(account) {
		panic("invalid account token: contains NATS wildcard characters")
	}
	return fmt.Sprintf("chat.user.%s.request.room.%s.%s.app.tabs", account, roomID, siteID)
}

// RoomAppTabsWildcard is the per-site subscription pattern for the
// GetRoomAppTabs RPC.
func RoomAppTabsWildcard(siteID string) string {
	return fmt.Sprintf("chat.user.*.request.room.*.%s.app.tabs", siteID)
}

// RoomAppCmdMenu returns the concrete subject for the
// GetRoomAppCommandMenu RPC.
func RoomAppCmdMenu(account, roomID, siteID string) string {
	if !isValidAccountToken(account) {
		panic("invalid account token: contains NATS wildcard characters")
	}
	return fmt.Sprintf("chat.user.%s.request.room.%s.%s.app.cmd-menu", account, roomID, siteID)
}

// RoomAppCmdMenuWildcard is the per-site subscription pattern for the
// GetRoomAppCommandMenu RPC.
func RoomAppCmdMenuWildcard(siteID string) string {
	return fmt.Sprintf("chat.user.*.request.room.*.%s.app.cmd-menu", siteID)
}

// RoomCreate: client→room-service create subject; siteID is the requester's site.
func RoomCreate(account, siteID string) string {
	return fmt.Sprintf("chat.user.%s.request.room.%s.create", account, siteID)
}

// RoomCreateWildcard is the queue-subscribe pattern for room-service.
func RoomCreateWildcard(siteID string) string {
	return fmt.Sprintf("chat.user.*.request.room.%s.create", siteID)
}

func RoomMemberEvent(roomID string) string {
	return fmt.Sprintf("chat.room.%s.event.member", roomID)
}

// RoomMemberEventWildcard matches member_added/member_removed events across all rooms.
func RoomMemberEventWildcard() string {
	return "chat.room.*.event.member"
}

func MsgThreadParentPattern(siteID string) string {
	return fmt.Sprintf("chat.user.{account}.request.room.{roomID}.%s.msg.thread.parent", siteID)
}

func MsgThreadParentWildcard(siteID string) string {
	return fmt.Sprintf("chat.user.*.request.room.*.%s.msg.thread.parent", siteID)
}

// --- search-service request/reply builders ---

// SearchMessages builds the concrete subject for a message search request; siteID routes to the correct search-service.
func SearchMessages(account, siteID string) string {
	return fmt.Sprintf("chat.user.%s.request.search.%s.messages", account, siteID)
}

// SearchRooms builds the concrete subject for a subscription search request.
func SearchRooms(account, siteID string) string {
	return fmt.Sprintf("chat.user.%s.request.search.%s.rooms", account, siteID)
}

// SearchMessagesPattern is the natsrouter pattern for message search; siteID baked in for per-site isolation.
func SearchMessagesPattern(siteID string) string {
	return fmt.Sprintf("chat.user.{account}.request.search.%s.messages", siteID)
}

// SearchRoomsPattern is the natsrouter pattern for subscription search.
func SearchRoomsPattern(siteID string) string {
	return fmt.Sprintf("chat.user.{account}.request.search.%s.rooms", siteID)
}

// SearchApps builds the concrete subject for an app search request.
func SearchApps(account, siteID string) string {
	return fmt.Sprintf("chat.user.%s.request.search.%s.apps", account, siteID)
}

// SearchAppsPattern is the natsrouter pattern for app search.
func SearchAppsPattern(siteID string) string {
	return fmt.Sprintf("chat.user.{account}.request.search.%s.apps", siteID)
}

// SearchUsers builds the concrete subject for a user search request.
func SearchUsers(account, siteID string) string {
	return fmt.Sprintf("chat.user.%s.request.search.%s.users", account, siteID)
}

// SearchUsersPattern is the natsrouter pattern for user search.
func SearchUsersPattern(siteID string) string {
	return fmt.Sprintf("chat.user.{account}.request.search.%s.users", siteID)
}

// --- room-service natsrouter pattern builders (siteID baked in) ---

func RoomCreatePattern(siteID string) string {
	return fmt.Sprintf("chat.user.{account}.request.room.%s.create", siteID)
}

func MemberRoleUpdatePattern(siteID string) string {
	return fmt.Sprintf("chat.user.{account}.request.room.{roomID}.%s.member.role-update", siteID)
}

func MemberRemovePattern(siteID string) string {
	return fmt.Sprintf("chat.user.{account}.request.room.{roomID}.%s.member.remove", siteID)
}

func MemberAddPattern(siteID string) string {
	return fmt.Sprintf("chat.user.{account}.request.room.{roomID}.%s.member.add", siteID)
}

func MemberListPattern(siteID string) string {
	return fmt.Sprintf("chat.user.{account}.request.room.{roomID}.%s.member.list", siteID)
}

func MemberStatusesPattern(siteID string) string {
	return fmt.Sprintf("chat.user.{account}.request.room.{roomID}.%s.member.statuses", siteID)
}

func MentionableSubscriptionsPattern(siteID string) string {
	return fmt.Sprintf("chat.user.{account}.request.room.{roomID}.%s.subscription.mentionable", siteID)
}

func OrgMembersPattern(siteID string) string {
	return fmt.Sprintf("chat.user.{account}.request.orgs.{orgID}.%s.members", siteID)
}

func MessageReadPattern(siteID string) string {
	return fmt.Sprintf("chat.user.{account}.request.room.{roomID}.%s.message.read", siteID)
}

func MessageReadReceiptPattern(siteID string) string {
	return fmt.Sprintf("chat.user.{account}.request.room.{roomID}.%s.message.read-receipt", siteID)
}

func MessageThreadReadPattern(siteID string) string {
	return fmt.Sprintf("chat.user.{account}.request.room.{roomID}.%s.message.thread.read", siteID)
}

func RoomKeyGetPattern(siteID string) string {
	return fmt.Sprintf("chat.user.{account}.request.room.{roomID}.%s.key.get", siteID)
}

func MuteTogglePattern(siteID string) string {
	return fmt.Sprintf("chat.user.{account}.request.room.{roomID}.%s.mute.toggle", siteID)
}

func FavoriteTogglePattern(siteID string) string {
	return fmt.Sprintf("chat.user.{account}.request.room.{roomID}.%s.favorite.toggle", siteID)
}

func RoomRenamePattern(siteID string) string {
	return fmt.Sprintf("chat.user.{account}.request.room.{roomID}.%s.room.rename", siteID)
}

func RoomAppTabsPattern(siteID string) string {
	return fmt.Sprintf("chat.user.{account}.request.room.{roomID}.%s.app.tabs", siteID)
}

func RoomAppCmdMenuPattern(siteID string) string {
	return fmt.Sprintf("chat.user.{account}.request.room.{roomID}.%s.app.cmd-menu", siteID)
}

// isValidAccountToken rejects empty tokens and tokens with NATS wildcard characters ('*' or '>').
func isValidAccountToken(token string) bool {
	return token != "" && !strings.ContainsAny(token, "*>")
}

// ParseRoomCreateSubject extracts the account from chat.user.{account}.request.room.{siteID}.create.
func ParseRoomCreateSubject(s string) (account string, ok bool) {
	parts := strings.Split(s, ".")
	if len(parts) != 7 {
		return "", false
	}
	if parts[0] != "chat" || parts[1] != "user" || parts[3] != "request" || parts[4] != "room" || parts[6] != "create" {
		return "", false
	}
	if !isValidAccountToken(parts[2]) {
		return "", false
	}
	return parts[2], true
}

// RoomCanonicalOperation returns the trailing op (e.g. "member.add") from chat.room.canonical.{siteID}.{op}.
func RoomCanonicalOperation(s string) (string, bool) {
	const prefix = "chat.room.canonical."
	if !strings.HasPrefix(s, prefix) {
		return "", false
	}
	rest := strings.TrimPrefix(s, prefix)
	dot := strings.IndexByte(rest, '.')
	if dot == -1 {
		return "", false
	}
	op := rest[dot+1:]
	if op == "" {
		return "", false
	}
	return op, true
}

// --- user-service builders ---

func UserStatusGetByName(account, siteID string) string {
	if !isValidAccountToken(account) {
		panic("invalid account token: contains NATS wildcard characters")
	}
	return fmt.Sprintf("chat.user.%s.request.user.%s.status.getByName", account, siteID)
}

func UserProfileGetByName(account, siteID string) string {
	if !isValidAccountToken(account) {
		panic("invalid account token: contains NATS wildcard characters")
	}
	return fmt.Sprintf("chat.user.%s.request.user.%s.profile.getByName", account, siteID)
}

func UserStatusSet(account, siteID string) string {
	if !isValidAccountToken(account) {
		panic("invalid account token: contains NATS wildcard characters")
	}
	return fmt.Sprintf("chat.user.%s.request.user.%s.status.set", account, siteID)
}

func UserSubscriptionGetChannels(account, siteID string) string {
	if !isValidAccountToken(account) {
		panic("invalid account token: contains NATS wildcard characters")
	}
	return fmt.Sprintf("chat.user.%s.request.user.%s.subscription.getChannels", account, siteID)
}

func UserSubscriptionGetDM(account, siteID string) string {
	if !isValidAccountToken(account) {
		panic("invalid account token: contains NATS wildcard characters")
	}
	return fmt.Sprintf("chat.user.%s.request.user.%s.subscription.getDM", account, siteID)
}

func UserSubscriptionCount(account, siteID string) string {
	if !isValidAccountToken(account) {
		panic("invalid account token: contains NATS wildcard characters")
	}
	return fmt.Sprintf("chat.user.%s.request.user.%s.subscription.count", account, siteID)
}

func UserAppsList(account, siteID string) string {
	if !isValidAccountToken(account) {
		panic("invalid account token: contains NATS wildcard characters")
	}
	return fmt.Sprintf("chat.user.%s.request.user.%s.apps.list", account, siteID)
}

// --- natsrouter pattern builders (siteID baked in, account left as {account} placeholder) ---

func UserStatusGetByNamePattern(siteID string) string {
	return fmt.Sprintf("chat.user.{account}.request.user.%s.status.getByName", siteID)
}

func UserProfileGetByNamePattern(siteID string) string {
	return fmt.Sprintf("chat.user.{account}.request.user.%s.profile.getByName", siteID)
}

func UserStatusSetPattern(siteID string) string {
	return fmt.Sprintf("chat.user.{account}.request.user.%s.status.set", siteID)
}

func UserSubscriptionGetChannelsPattern(siteID string) string {
	return fmt.Sprintf("chat.user.{account}.request.user.%s.subscription.getChannels", siteID)
}

func UserSubscriptionGetDMPattern(siteID string) string {
	return fmt.Sprintf("chat.user.{account}.request.user.%s.subscription.getDM", siteID)
}

func UserSubscriptionCountPattern(siteID string) string {
	return fmt.Sprintf("chat.user.{account}.request.user.%s.subscription.count", siteID)
}

func UserAppsListPattern(siteID string) string {
	return fmt.Sprintf("chat.user.{account}.request.user.%s.apps.list", siteID)
}

func UserSubscriptionList(account, siteID string) string {
	if !isValidAccountToken(account) {
		panic("invalid account token: contains NATS wildcard characters")
	}
	return fmt.Sprintf("chat.user.%s.request.user.%s.subscription.list", account, siteID)
}

func UserSubscriptionListPattern(siteID string) string {
	return fmt.Sprintf("chat.user.{account}.request.user.%s.subscription.list", siteID)
}

func UserSubscriptionSetAppSubscription(account, siteID string) string {
	if !isValidAccountToken(account) {
		panic("invalid account token: contains NATS wildcard characters")
	}
	return fmt.Sprintf("chat.user.%s.request.user.%s.subscription.setAppSubscription", account, siteID)
}

func UserSubscriptionSetAppSubscriptionPattern(siteID string) string {
	return fmt.Sprintf("chat.user.{account}.request.user.%s.subscription.setAppSubscription", siteID)
}

func UserSubscriptionGetByRoomID(account, siteID string) string {
	if !isValidAccountToken(account) {
		panic("invalid account token: contains NATS wildcard characters")
	}
	return fmt.Sprintf("chat.user.%s.request.user.%s.subscription.getByRoomID", account, siteID)
}

func UserSubscriptionGetByRoomIDPattern(siteID string) string {
	return fmt.Sprintf("chat.user.{account}.request.user.%s.subscription.getByRoomID", siteID)
}

// ParseUserSubject parses any 8-token subject "chat.user.{account}.request.user.{siteID}.{area}.{action}"
// where area is one of "status", "subscription", "profile", "apps". Use ParseRoomSubject for room-scoped forms.
func ParseUserSubject(subj string) (account, siteID, area, action string, ok bool) {
	parts := strings.Split(subj, ".")
	if len(parts) != 8 {
		return "", "", "", "", false
	}
	if parts[0] != "chat" || parts[1] != "user" || parts[3] != "request" || parts[4] != "user" {
		return "", "", "", "", false
	}
	if !isValidAccountToken(parts[2]) {
		return "", "", "", "", false
	}
	switch parts[6] {
	case "status", "subscription", "profile", "apps":
	default:
		return "", "", "", "", false
	}
	return parts[2], parts[5], parts[6], parts[7], true
}

func ParseStatusSubject(subj string) (account, action string, ok bool) {
	a, _, area, act, k := ParseUserSubject(subj)
	if !k || area != "status" {
		return "", "", false
	}
	return a, act, true
}

func ParseSubscriptionSubject(subj string) (account, action string, ok bool) {
	a, _, area, act, k := ParseUserSubject(subj)
	if !k || area != "subscription" {
		return "", "", false
	}
	return a, act, true
}

func ParseAppsSubject(subj string) (account, action string, ok bool) {
	a, _, area, act, k := ParseUserSubject(subj)
	if !k || area != "apps" {
		return "", "", false
	}
	return a, act, true
}

// ParseRoomSubject parses "chat.user.{account}.request.user.{siteID}.room.{roomID}.{area}.{action}" (10 tokens).
// Returns ok=false if not exactly 10 tokens or the prefix doesn't match.
func ParseRoomSubject(subj string) (account, roomID, action string, ok bool) {
	parts := strings.Split(subj, ".")
	if len(parts) != 10 {
		return "", "", "", false
	}
	if parts[0] != "chat" || parts[1] != "user" || parts[3] != "request" || parts[4] != "user" || parts[6] != "room" {
		return "", "", "", false
	}
	if !isValidAccountToken(parts[2]) {
		return "", "", "", false
	}
	return parts[2], parts[7], parts[9], true
}

func UserStatusWildCard(siteID string) string {
	return fmt.Sprintf("chat.user.*.request.user.%s.status.>", siteID)
}

func UserSubscriptionWildCard(siteID string) string {
	return fmt.Sprintf("chat.user.*.request.user.%s.subscription.>", siteID)
}

func UserProfileWildCard(siteID string) string {
	return fmt.Sprintf("chat.user.*.request.user.%s.profile.>", siteID)
}

func UserRoomWildCard(siteID string) string {
	return fmt.Sprintf("chat.user.*.request.user.%s.room.>", siteID)
}

func UserAppsWildCard(siteID string) string {
	return fmt.Sprintf("chat.user.*.request.user.%s.apps.>", siteID)
}

// PushNotification is the per-recipient mobile-push subject under chat.server.* (client JWTs cannot subscribe).
func PushNotification(siteID string) string {
	return fmt.Sprintf("chat.server.notification.push.%s.send", siteID)
}

// PushNotificationFilter is the stream-binding wildcard covering .send and any future siblings.
func PushNotificationFilter(siteID string) string {
	return fmt.Sprintf("chat.server.notification.push.%s.>", siteID)
}

// ServerBroadcastThreadTCount is the subject for thread reply-count badge events; fire-and-forget, not in MESSAGES_CANONICAL.
func ServerBroadcastThreadTCount(siteID string) string {
	return fmt.Sprintf("chat.server.broadcast.%s.thread.tcount", siteID)
}

// ServerBroadcastWildcard is the queue-subscribe subject used by broadcast-worker
// to receive all server-broadcast events for a site.
func ServerBroadcastWildcard(siteID string) string {
	return fmt.Sprintf("chat.server.broadcast.%s.>", siteID)
}

// PresenceSnapshot is the bulk presence RPC subject (request/reply).
func PresenceSnapshot(siteID string) string {
	return fmt.Sprintf("chat.presence.%s.request.snapshot", siteID)
}

// SubscriptionUpdateWildcard matches every subscription.update fanout event.
func SubscriptionUpdateWildcard() string {
	return "chat.user.*.event.subscription.update"
}

// ParseSubscriptionUpdateAccount extracts the account from a subscription.update subject.
func ParseSubscriptionUpdateAccount(s string) (account string, ok bool) {
	parts := strings.Split(s, ".")
	if len(parts) != 6 {
		return "", false
	}
	if parts[0] != "chat" || parts[1] != "user" || parts[3] != "event" ||
		parts[4] != "subscription" || parts[5] != "update" {
		return "", false
	}
	if !isValidAccountToken(parts[2]) {
		return "", false
	}
	return parts[2], true
}
