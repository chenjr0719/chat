package service

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/hmchangw/chat/pkg/errcode"
	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/natsrouter"
	"github.com/hmchangw/chat/pkg/natsutil"
	"github.com/hmchangw/chat/user-service/models"
)

var validListTypes = map[string]bool{"current": true, "rooms": true, "apps": true}

// maxSiteFanout bounds concurrent per-site room-service RPCs — otherwise a
// heavily-federated ALL_SITE_IDS fans one request into N simultaneous 5s RPCs.
const maxSiteFanout = 8

// DM-target markers rejected by GetDM: platform/system accounts are prefixed
// "p_" and bot accounts end in ".bot" — neither is a valid human DM counterpart.
const (
	dmTargetSystemPrefix = "p_"
	dmTargetBotSuffix    = ".bot"
)

// maxAccountNames caps getChannels' accountNames — unbounded input builds an arbitrarily large $in/$setIsSubset operand.
const maxAccountNames = 100

func (s *UserService) ListSubscriptions(c *natsrouter.Context, req models.SubscriptionListRequest) (*models.SubscriptionListResponse, error) {
	if !validListTypes[req.Type] {
		return nil, errcode.BadRequest("unknown subscription type")
	}
	if req.UpdatedWithinDays != nil && *req.UpdatedWithinDays < 0 {
		// A negative window computes a FUTURE cutoff and silently returns empty.
		return nil, errcode.BadRequest("updatedWithinDays must be non-negative")
	}
	account := c.Param("account")
	c.WithLogValues("account", account)
	subs, err := s.subs.AggregateSubscriptions(c, account, req.Type, req.UpdatedWithinDays, s.maxSubs)
	if err != nil {
		return nil, fmt.Errorf("list subscriptions: %w", err)
	}
	if req.Favorite != nil && *req.Favorite {
		subs = filterFavorites(subs)
		subs = moveSelfDMFront(subs, account)
	}
	s.applyAppDisplayNames(c, subs)
	s.enrichWithRoomInfo(c, subs)
	return &models.SubscriptionListResponse{Subscriptions: subs, Total: len(subs)}, nil
}

// applyAppDisplayNames swaps each botDM subscription's name (the bot account) for the
// app's display name — before enrichment, which never touches subscription names.
// Lookup failure or a missing app degrades to the stored account name.
func (s *UserService) applyAppDisplayNames(c *natsrouter.Context, subs []model.Subscription) {
	var bots []string
	seen := map[string]struct{}{}
	for i := range subs {
		if subs[i].RoomType != model.RoomTypeBotDM {
			continue
		}
		if _, dup := seen[subs[i].Name]; dup {
			continue
		}
		seen[subs[i].Name] = struct{}{}
		bots = append(bots, subs[i].Name)
	}
	if len(bots) == 0 {
		return
	}
	names, err := s.apps.GetAppNamesByAssistants(c, bots)
	if err != nil {
		slog.WarnContext(c, "app display-name lookup degraded", "account", c.Param("account"), "request_id", natsutil.RequestIDFromContext(c), "error", err)
		return
	}
	for i := range subs {
		if subs[i].RoomType != model.RoomTypeBotDM {
			continue
		}
		if name, ok := names[subs[i].Name]; ok && name != "" {
			subs[i].Name = name
		}
	}
}

// enrichWithRoomInfo overwrites room-derived fields per site in parallel; a failed
// site RPC keeps that site's subs unenriched — NOT count's all-or-nothing fallback.
func (s *UserService) enrichWithRoomInfo(c *natsrouter.Context, subs []model.Subscription) {
	if len(subs) == 0 {
		return
	}
	idxBySite := map[string][]int{}
	for i := range subs {
		idxBySite[subs[i].SiteID] = append(idxBySite[subs[i].SiteID], i)
	}
	sites := make([]string, 0, len(idxBySite))
	for site := range idxBySite {
		sites = append(sites, site)
	}
	infoBySite := make([]map[string]model.RoomInfo, len(sites)) // nil ⇒ site degraded
	// WaitGroup (not errgroup): errgroup.WithContext would cancel sibling site RPCs on the first error; per-site degradation must keep siblings running.
	// Acquire sem BEFORE spawning so live goroutine COUNT (not just concurrency) stays ≤ maxSiteFanout — a wide federation otherwise spawns one parked goroutine per site.
	var wg sync.WaitGroup
	sem := make(chan struct{}, maxSiteFanout)
	for i, site := range sites {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			roomIDs := make([]string, 0, len(idxBySite[site]))
			seen := make(map[string]struct{}, len(idxBySite[site]))
			for _, j := range idxBySite[site] {
				rid := subs[j].RoomID
				if _, dup := seen[rid]; dup {
					continue
				}
				seen[rid] = struct{}{}
				roomIDs = append(roomIDs, rid)
			}
			infos, err := s.rooms.GetRoomsInfo(c, site, roomIDs)
			if err != nil {
				slog.WarnContext(c, "room-info enrichment degraded", "account", c.Param("account"), "site", site, "request_id", natsutil.RequestIDFromContext(c), "error", err)
				return
			}
			m := make(map[string]model.RoomInfo, len(infos))
			for i := range infos {
				m[infos[i].RoomID] = infos[i]
			}
			infoBySite[i] = m
		}()
	}
	wg.Wait()
	for i, site := range sites {
		m := infoBySite[i]
		if m == nil {
			continue
		}
		for _, j := range idxBySite[site] {
			info := m[subs[j].RoomID]
			applyRoomInfo(&subs[j], &info)
		}
	}
	// Degraded site or room not found ⇒ baseline room from the Mongo $lookup
	// values, so the client always receives a room object.
	for i := range subs {
		if subs[i].Room == nil {
			subs[i].Room = &model.SubscriptionRoom{
				SiteID:    subs[i].SiteID,
				UserCount: subs[i].UserCount,
				LastMsgAt: subs[i].LastMsgAt,
				LastMsgID: subs[i].LastMsgID,
			}
		}
	}
}

// applyRoomInfo nests all room-derived fields (including the E2E key for initial
// key bootstrap) under sub.Room and computes alert/hasMention; zero-value info
// (Found=false) is skipped. The subscription's own name is never overwritten —
// room-service only supplies the room's canonical name.
func applyRoomInfo(sub *model.Subscription, info *model.RoomInfo) {
	if !info.Found {
		return
	}
	room := &model.SubscriptionRoom{
		SiteID:     info.SiteID,
		Name:       info.Name,
		UserCount:  info.UserCount,
		AppCount:   info.AppCount,
		LastMsgID:  info.LastMsgID,
		PrivateKey: info.PrivateKey,
		KeyVersion: info.KeyVersion,
	}
	if info.LastMsgAt != nil {
		t := time.UnixMilli(*info.LastMsgAt).UTC()
		room.LastMsgAt = &t
	}
	if info.LastMentionAllAt != nil {
		t := time.UnixMilli(*info.LastMentionAllAt).UTC()
		room.LastMentionAllAt = &t
	}
	sub.Room = room
	// alert = hasUnread, hasMention = hasGroupMention (legacy wire-name mapping).
	sub.Alert = unread(sub.LastSeenAt, info.LastMsgAt)
	sub.HasMention = unread(sub.LastSeenAt, info.LastMentionAllAt)
}

// unread: a room event at ms (epoch millis) is newer than lastSeen; nil ms ⇒ false, nil lastSeen with ms set ⇒ true.
func unread(lastSeen *time.Time, ms *int64) bool {
	if ms == nil {
		return false
	}
	if lastSeen == nil {
		return true
	}
	return lastSeen.UTC().UnixMilli() < *ms
}

func filterFavorites(subs []model.Subscription) []model.Subscription {
	// [:0:0] — cap 0 forces a fresh backing array so append never aliases/mutates the input
	out := subs[:0:0]
	for i := range subs {
		if subs[i].Favorite {
			out = append(out, subs[i])
		}
	}
	return out
}

func moveSelfDMFront(subs []model.Subscription, account string) []model.Subscription {
	for i := range subs {
		if subs[i].RoomType == model.RoomTypeDM && subs[i].Name == account {
			out := make([]model.Subscription, 0, len(subs))
			out = append(out, subs[i])
			out = append(out, subs[:i]...)
			return append(out, subs[i+1:]...)
		}
	}
	return subs
}

func (s *UserService) GetChannels(c *natsrouter.Context, req models.GetChannelsRequest) (*models.SubscriptionListResponse, error) {
	account := c.Param("account")
	c.WithLogValues("account", account)
	hasContain, hasNames := req.MembersContain != "", len(req.AccountNames) > 0
	if hasContain == hasNames {
		return nil, errcode.BadRequest("exactly one of membersContain or accountNames is required")
	}
	if len(req.AccountNames) > maxAccountNames {
		return nil, errcode.BadRequest("too many accountNames")
	}
	members := req.AccountNames
	if hasContain {
		members = []string{req.MembersContain}
	}
	subs, err := s.subs.FindChannelsByMembers(c, account, members, s.maxSubs)
	if err != nil {
		return nil, fmt.Errorf("get channels: %w", err)
	}
	s.enrichWithRoomInfo(c, subs)
	return &models.SubscriptionListResponse{Subscriptions: subs, Total: len(subs)}, nil
}

func (s *UserService) GetDM(c *natsrouter.Context, req models.GetDMRequest) (*models.DMResponse, error) {
	account := c.Param("account")
	c.WithLogValues("account", account, "target", req.AccountName)
	if req.AccountName == "" {
		return nil, errcode.BadRequest("accountName required")
	}
	if strings.HasPrefix(req.AccountName, dmTargetSystemPrefix) || strings.HasSuffix(req.AccountName, dmTargetBotSuffix) {
		return nil, errcode.BadRequest("invalid DM target", errcode.WithReason(errcode.UserInvalidDMTarget))
	}
	dm, err := s.subs.GetDMSubscription(c, account, req.AccountName)
	if err != nil {
		return nil, fmt.Errorf("get dm: %w", err)
	}
	if dm == nil {
		return nil, errcode.NotFound("dm not found", errcode.WithReason(errcode.UserSubscriptionNotFound))
	}
	if dm.Subscription == nil {
		return nil, errcode.Internal("malformed dm subscription")
	}
	// enrichWithRoomInfo mutates slice elements in place, so the single embedded sub is boxed into a 1-elem slice to receive the update.
	one := []model.Subscription{*dm.Subscription}
	s.enrichWithRoomInfo(c, one)
	out := *dm
	out.Subscription = &one[0]
	return &models.DMResponse{Subscription: out}, nil
}

// GetByRoomID returns the caller's room-info-enriched subscription for req.RoomID
// as a 0-or-1-element list (empty = not subscribed; absence is a normal answer).
func (s *UserService) GetByRoomID(c *natsrouter.Context, req models.GetByRoomIDRequest) (*models.SubscriptionListResponse, error) {
	account := c.Param("account")
	c.WithLogValues("account", account, "roomId", req.RoomID)
	if req.RoomID == "" {
		return nil, errcode.BadRequest("roomId required")
	}
	sub, err := s.subs.GetSubscriptionByRoomID(c, account, req.RoomID)
	if err != nil {
		return nil, fmt.Errorf("get subscription by roomId: %w", err)
	}
	if sub == nil {
		return &models.SubscriptionListResponse{Subscriptions: []model.Subscription{}, Total: 0}, nil
	}
	one := []model.Subscription{*sub}
	s.applyAppDisplayNames(c, one)
	s.enrichWithRoomInfo(c, one)
	return &models.SubscriptionListResponse{Subscriptions: one, Total: 1}, nil
}

func (s *UserService) CountSubscriptions(c *natsrouter.Context, req models.CountRequest) (*models.CountResponse, error) {
	account := c.Param("account")
	c.WithLogValues("account", account)
	total, err := s.subs.CountActiveSubscriptions(c, account)
	if err != nil {
		return nil, fmt.Errorf("count subscriptions: %w", err)
	}
	if req.Unread == nil || !*req.Unread {
		return &models.CountResponse{Count: total}, nil
	}
	return s.countUnread(c, account, total)
}

// countUnread counts active subs with unread messages via per-site GetRoomsInfo RPCs
// (fail-fast); any site failure falls back to total — a partial count would mislead.
func (s *UserService) countUnread(ctx context.Context, account string, total int) (*models.CountResponse, error) {
	// Short-circuit zero: min(0, maxSubs)=0 would build a $limit:0 MongoDB rejects.
	if total == 0 {
		return &models.CountResponse{Count: 0}, nil
	}
	// Cap at maxSubs — query-side total can exceed the cap; min keeps the fetch bounded and consistent with the list endpoints.
	subs, err := s.subs.GetActiveSubscriptions(ctx, account, min(total, s.maxSubs))
	if err != nil {
		return nil, fmt.Errorf("count unread: %w", err)
	}
	bySite := map[string][]model.Subscription{}
	for i := range subs {
		bySite[subs[i].SiteID] = append(bySite[subs[i].SiteID], subs[i])
	}
	sites := make([]string, 0, len(bySite))
	for site := range bySite {
		sites = append(sites, site)
	}
	results := make([]int, len(sites))
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(maxSiteFanout) // bound concurrent per-site RPCs
	for i, site := range sites {
		g.Go(func() error {
			siteSubs := bySite[site]
			roomIDs := make([]string, 0, len(siteSubs))
			seenRooms := make(map[string]struct{}, len(siteSubs))
			for j := range siteSubs {
				rid := siteSubs[j].RoomID
				if _, dup := seenRooms[rid]; dup {
					continue
				}
				seenRooms[rid] = struct{}{}
				roomIDs = append(roomIDs, rid)
			}
			infos, err := s.rooms.GetRoomsInfo(gctx, site, roomIDs)
			if err != nil {
				return fmt.Errorf("unread count rooms-info for site %s: %w", site, err)
			}
			lastMsg := make(map[string]*int64, len(infos))
			for i := range infos {
				lastMsg[infos[i].RoomID] = infos[i].LastMsgAt
			}
			n := 0
			for j := range siteSubs {
				if unread(siteSubs[j].LastSeenAt, lastMsg[siteSubs[j].RoomID]) {
					n++
				}
			}
			results[i] = n
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		slog.WarnContext(ctx, "unread count fell back to total", "account", account, "request_id", natsutil.RequestIDFromContext(ctx), "error", err)
		return &models.CountResponse{Count: total}, nil
	}
	unreadTotal := 0
	for _, n := range results {
		unreadTotal += n
	}
	return &models.CountResponse{Count: unreadTotal}, nil
}
