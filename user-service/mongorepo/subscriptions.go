package mongorepo

import (
	"context"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/mongoutil"
)

const subscriptionsCollection = "subscriptions"

// roomsCollection is the $lookup target for the deleted-filter and enrichment; owned by room-service, referenced only by name.
const roomsCollection = "rooms"

// deletedRoomNameRegex matches room-service's soft-delete rename ("Del-"+name); the deleted-filter excludes matching local subs.
const deletedRoomNameRegex = "^Del-"

// SubscriptionRepo is the Mongo implementation of service.SubscriptionRepository.
type SubscriptionRepo struct {
	subscriptions *mongoutil.Collection[model.Subscription]
	siteID        string // this instance's site — distinguishes local vs cross-site rows in the deleted-filter
}

// NewSubscriptionRepo builds a SubscriptionRepo over db; the deleted-filter keeps cross-site rows, drops local rows with missing/soft-deleted rooms.
func NewSubscriptionRepo(db *mongo.Database, siteID string) *SubscriptionRepo {
	return &SubscriptionRepo{
		subscriptions: mongoutil.NewCollection[model.Subscription](db.Collection(subscriptionsCollection)),
		siteID:        siteID,
	}
}

// EnsureIndexes creates the subscription indexes this service queries on.
func (r *SubscriptionRepo) EnsureIndexes(ctx context.Context) error {
	if _, err := r.subscriptions.Raw().Indexes().CreateMany(ctx, []mongo.IndexModel{
		// Serves the account+roomType match on every list/count path; the retention
		// window keys on room.lastMsgAt (a room field), so no trailing time key.
		{Keys: bson.D{{Key: "u.account", Value: 1}, {Key: "roomType", Value: 1}}},
		// Unique logical key (one subscription per room per user). Must match
		// room-service's declaration on the shared collection (mismatch → conflict).
		{Keys: bson.D{{Key: "roomId", Value: 1}, {Key: "u.account", Value: 1}}, Options: options.Index().SetUnique(true)},
		{Keys: bson.D{{Key: "name", Value: 1}, {Key: "roomType", Value: 1}}},
	}); err != nil {
		return fmt.Errorf("create subscription indexes: %w", err)
	}
	return nil
}

// roomsEnrichStages builds the shared rooms-join + deleted-filter + optional
// activity window (local rows need room.lastMsgAt >= cutoff; cross-site kept).
func roomsEnrichStages(localSiteID string, windowCutoff *time.Time) bson.A {
	stages := bson.A{
		bson.M{"$lookup": bson.M{"from": roomsCollection, "localField": "roomId", "foreignField": "_id", "as": "room"}},
		bson.M{"$unwind": bson.M{"path": "$room", "preserveNullAndEmptyArrays": true}},
		bson.M{"$match": bson.M{"$or": bson.A{
			bson.M{"siteId": bson.M{"$ne": localSiteID}}, // cross-site: keep regardless
			bson.M{"$and": bson.A{ // local: room must exist AND not be Del-prefixed
				bson.M{"room": bson.M{"$ne": nil}},
				bson.M{"room.name": bson.M{"$not": bson.M{"$regex": deletedRoomNameRegex}}},
			}},
		}}},
	}
	if windowCutoff != nil {
		stages = append(stages, bson.M{"$match": bson.M{"$or": bson.A{
			bson.M{"siteId": bson.M{"$ne": localSiteID}},            // cross-site: keep regardless
			bson.M{"room.lastMsgAt": bson.M{"$gte": *windowCutoff}}, // local: room active within N days
		}}})
	}
	return append(stages,
		bson.M{"$addFields": bson.M{
			"userCount":        "$room.userCount",
			"lastMsgAt":        "$room.lastMsgAt",
			"lastMsgId":        "$room.lastMsgId",
			"lastMentionAllAt": "$room.lastMentionAllAt",
		}},
		bson.M{"$project": bson.M{"room": 0}},
	)
}

// AggregateSubscriptions lists account's subscriptions by listType: rooms (dm+channel), apps (subscribed botDMs), or current (merged $facet set).
func (r *SubscriptionRepo) AggregateSubscriptions(ctx context.Context, account, listType string, withinDays *int, limit int) ([]model.Subscription, error) {
	if listType == "current" {
		return r.aggregateCurrent(ctx, account, limit)
	}
	match := bson.M{"u.account": account, "muted": bson.M{"$ne": true}}
	var windowCutoff *time.Time
	switch listType {
	case "rooms":
		match["roomType"] = bson.M{"$in": bson.A{"dm", "channel"}}
		if withinDays != nil {
			// Windows on whole-room activity (room.lastMsgAt) post-$lookup — subscriptions carry no updatedAt.
			cutoff := time.Now().UTC().AddDate(0, 0, -*withinDays)
			windowCutoff = &cutoff
		}
	case "apps":
		// withinDays is intentionally not applied to apps subscriptions.
		match["roomType"] = "botDM"
		match["isSubscribed"] = true
	}
	pipeline := bson.A{bson.M{"$match": match}}
	pipeline = append(pipeline, roomsEnrichStages(r.siteID, windowCutoff)...)
	pipeline = append(pipeline,
		bson.M{"$sort": bson.D{{Key: "favorite", Value: -1}, {Key: "name", Value: 1}}},
		bson.M{"$limit": int64(limit)},
	)
	return r.subscriptions.Aggregate(ctx, pipeline, mongoutil.WithAllowDiskUse())
}

// aggregateCurrent merges the rooms (dm/channel) and apps (botDM) $facet branches — each needs a different roomType $match; no window.
func (r *SubscriptionRepo) aggregateCurrent(ctx context.Context, account string, limit int) ([]model.Subscription, error) {
	match := bson.M{"u.account": account, "$or": bson.A{
		bson.M{"roomType": bson.M{"$in": bson.A{"dm", "channel"}}, "muted": bson.M{"$ne": true}},
		bson.M{"roomType": "botDM", "muted": bson.M{"$ne": true}, "isSubscribed": true},
	}}
	pipeline := bson.A{bson.M{"$match": match}}
	pipeline = append(pipeline, roomsEnrichStages(r.siteID, nil)...)
	sortStage := bson.M{"$sort": bson.D{{Key: "favorite", Value: -1}, {Key: "name", Value: 1}}}
	limitStage := bson.M{"$limit": int64(limit)}
	pipeline = append(pipeline,
		bson.M{"$facet": bson.M{
			// Branches sort+limit BEFORE the merge so the post-concat $sort sees
			// ≤ 2*limit docs — the global top-K is within the union of branch top-Ks.
			"rooms": bson.A{
				bson.M{"$match": bson.M{"roomType": bson.M{"$in": bson.A{"dm", "channel"}}}},
				sortStage, limitStage,
			},
			"apps": bson.A{
				bson.M{"$match": bson.M{"roomType": "botDM"}},
				sortStage, limitStage,
			},
		}},
		bson.M{"$project": bson.M{"all": bson.M{"$concatArrays": bson.A{"$rooms", "$apps"}}}},
		bson.M{"$unwind": "$all"},
		bson.M{"$replaceRoot": bson.M{"newRoot": "$all"}},
		sortStage,
		limitStage,
	)
	return r.subscriptions.Aggregate(ctx, pipeline, mongoutil.WithAllowDiskUse())
}

// FindChannelsByMembers returns the requester's channel subs whose room contains ALL given members (bots excluded), room.createdAt desc.
// Inlines roomsEnrichStages so that sort runs while "room" is still present.
func (r *SubscriptionRepo) FindChannelsByMembers(ctx context.Context, account string, members []string, limit int) ([]model.Subscription, error) {
	localSiteID := r.siteID
	pipeline := bson.A{
		bson.M{"$match": bson.M{"u.account": account, "roomType": "channel", "muted": bson.M{"$ne": true}}},
		// Co-member join — NOT siteId-filtered (any local/federated sub counts), projected
		// to u.account only. members is $literal-wrapped: $-values read as literals, not field paths.
		bson.M{"$lookup": bson.M{
			"from": subscriptionsCollection,
			"let":  bson.M{"rid": "$roomId"},
			"pipeline": bson.A{
				bson.M{"$match": bson.M{"$expr": bson.M{"$and": bson.A{
					bson.M{"$eq": bson.A{"$roomId", "$$rid"}},
					bson.M{"$ne": bson.A{"$u.isBot", true}},
					bson.M{"$in": bson.A{"$u.account", bson.M{"$literal": members}}},
				}}}},
				bson.M{"$project": bson.M{"_id": 0, "u.account": 1}},
			},
			"as": "members",
		}},
		// Require every requested member present; members $literal-wrapped as above.
		bson.M{"$match": bson.M{"$expr": bson.M{"$setIsSubset": bson.A{
			bson.M{"$literal": members},
			bson.M{"$map": bson.M{"input": "$members", "as": "m", "in": "$$m.u.account"}},
		}}}},
		bson.M{"$project": bson.M{"members": 0}},
		// Join local rooms for the deleted-filter and the createdAt sort below.
		bson.M{"$lookup": bson.M{"from": roomsCollection, "localField": "roomId", "foreignField": "_id", "as": "room"}},
		bson.M{"$unwind": bson.M{"path": "$room", "preserveNullAndEmptyArrays": true}},
		// Deleted-filter: drop local subs with missing/soft-deleted rooms; keep cross-site.
		bson.M{"$match": bson.M{"$or": bson.A{
			bson.M{"siteId": bson.M{"$ne": localSiteID}},
			bson.M{"$and": bson.A{
				bson.M{"room": bson.M{"$ne": nil}},
				bson.M{"room.name": bson.M{"$not": bson.M{"$regex": deletedRoomNameRegex}}},
			}},
		}}},
		// Sort by the room's createdAt DESC while "room" is still available.
		bson.M{"$sort": bson.D{{Key: "room.createdAt", Value: -1}}},
		bson.M{"$addFields": bson.M{
			"userCount":        "$room.userCount",
			"lastMsgAt":        "$room.lastMsgAt",
			"lastMsgId":        "$room.lastMsgId",
			"lastMentionAllAt": "$room.lastMentionAllAt",
		}},
		bson.M{"$project": bson.M{"room": 0}},
		bson.M{"$limit": int64(limit)},
	}
	return r.subscriptions.Aggregate(ctx, pipeline, mongoutil.WithAllowDiskUse())
}

// GetDMSubscription returns the requester's room-enriched DM sub with target plus the counterpart's HRInfo (cross-site ⇒ nil), or (nil, nil).
func (r *SubscriptionRepo) GetDMSubscription(ctx context.Context, account, target string) (*model.DMSubscription, error) {
	pipeline := bson.A{
		bson.M{"$match": bson.M{"u.account": account, "name": target, "roomType": "dm"}},
	}
	pipeline = append(pipeline, roomsEnrichStages(r.siteID, nil)...)
	pipeline = append(pipeline,
		bson.M{"$lookup": bson.M{"from": usersCollection, "localField": "name", "foreignField": "account", "as": "hrUser"}},
		bson.M{"$unwind": bson.M{"path": "$hrUser", "preserveNullAndEmptyArrays": true}},
		bson.M{"$addFields": bson.M{"hrInfo": bson.M{"$cond": bson.A{
			bson.M{"$ifNull": bson.A{"$hrUser", false}},
			bson.M{
				"account": "$hrUser.account",
				// HRInfo.Name carries the Chinese (native) name — User has no plain "name".
				"name":    "$hrUser.chineseName",
				"engName": "$hrUser.engName",
			},
			"$$REMOVE",
		}}}},
		bson.M{"$project": bson.M{"hrUser": 0}},
	)
	// .Raw(): decodes into []model.DMSubscription, not []model.Subscription.
	cur, err := r.subscriptions.Raw().Aggregate(ctx, pipeline, options.Aggregate().SetAllowDiskUse(true))
	if err != nil {
		return nil, fmt.Errorf("aggregate dm subscription: %w", err)
	}
	var out []model.DMSubscription
	if err := cur.All(ctx, &out); err != nil {
		return nil, fmt.Errorf("decode dm subscription: %w", err)
	}
	if len(out) == 0 {
		return nil, nil
	}
	return &out[0], nil
}

// GetSubscriptionByRoomID returns the requester's deleted-filtered sub for roomID, or (nil, nil); (account, roomId) is unique in practice.
func (r *SubscriptionRepo) GetSubscriptionByRoomID(ctx context.Context, account, roomID string) (*model.Subscription, error) {
	pipeline := bson.A{bson.M{"$match": bson.M{"u.account": account, "roomId": roomID}}}
	pipeline = append(pipeline, roomsEnrichStages(r.siteID, nil)...)
	out, err := r.subscriptions.Aggregate(ctx, pipeline, mongoutil.WithAllowDiskUse())
	if err != nil {
		return nil, fmt.Errorf("aggregate subscription by roomId: %w", err)
	}
	if len(out) == 0 {
		return nil, nil
	}
	return &out[0], nil
}

// activeSubscriptionFilter: non-muted dm/channel subs, or non-muted subscribed botDMs (the count endpoints' notion of active).
func activeSubscriptionFilter(account string) bson.M {
	return bson.M{"u.account": account, "$or": bson.A{
		bson.M{"roomType": bson.M{"$in": bson.A{"dm", "channel"}}, "muted": bson.M{"$ne": true}},
		bson.M{"roomType": "botDM", "muted": bson.M{"$ne": true}, "isSubscribed": true},
	}}
}

// CountActiveSubscriptions counts the deleted-filtered active set via $count over the enriched pipeline (CountDocuments cannot see the join).
func (r *SubscriptionRepo) CountActiveSubscriptions(ctx context.Context, account string) (int, error) {
	pipeline := bson.A{bson.M{"$match": activeSubscriptionFilter(account)}}
	pipeline = append(pipeline, roomsEnrichStages(r.siteID, nil)...)
	pipeline = append(pipeline, bson.M{"$count": "n"})
	cur, err := r.subscriptions.Raw().Aggregate(ctx, pipeline, options.Aggregate().SetAllowDiskUse(true))
	if err != nil {
		return 0, fmt.Errorf("count active subscriptions: %w", err)
	}
	var out []struct {
		N int `bson:"n"`
	}
	if err := cur.All(ctx, &out); err != nil {
		return 0, fmt.Errorf("decode active subscription count: %w", err)
	}
	if len(out) == 0 {
		return 0, nil
	}
	return out[0].N, nil
}

// GetActiveSubscriptions returns the deleted-filtered active set used by the unread count, capped by limit.
func (r *SubscriptionRepo) GetActiveSubscriptions(ctx context.Context, account string, limit int) ([]model.Subscription, error) {
	pipeline := bson.A{bson.M{"$match": activeSubscriptionFilter(account)}}
	pipeline = append(pipeline, roomsEnrichStages(r.siteID, nil)...)
	// MongoDB rejects $limit:0 — callers short-circuit zero; stay defensive here.
	if limit > 0 {
		pipeline = append(pipeline, bson.M{"$limit": int64(limit)})
	}
	return r.subscriptions.Aggregate(ctx, pipeline, mongoutil.WithAllowDiskUse())
}

// GetAppSubscription returns the requester's botDM subscription for botName, or (nil, nil).
func (r *SubscriptionRepo) GetAppSubscription(ctx context.Context, account, botName string) (*model.Subscription, error) {
	return r.subscriptions.FindOne(ctx, bson.M{"u.account": account, "name": botName, "roomType": "botDM"})
}

// SetAppSubscribed updates isSubscribed/muted on the requester's botDM subscription.
func (r *SubscriptionRepo) SetAppSubscribed(ctx context.Context, account, botName string, subscribed, muted bool) error {
	if _, err := r.subscriptions.Raw().UpdateOne(ctx,
		bson.M{"u.account": account, "name": botName, "roomType": "botDM"},
		bson.M{"$set": bson.M{"isSubscribed": subscribed, "muted": muted}},
	); err != nil {
		return fmt.Errorf("update app subscription: %w", err)
	}
	return nil
}
