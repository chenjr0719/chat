package mongorepo

import (
	"context"
	"errors"
	"fmt"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/mongoutil"
)

const usersCollection = "users"

// UserRepo is the Mongo implementation of service.UserRepository.
type UserRepo struct {
	users *mongoutil.Collection[model.User]
}

// NewUserRepo builds a UserRepo over db.
func NewUserRepo(db *mongo.Database) *UserRepo {
	return &UserRepo{
		users: mongoutil.NewCollection[model.User](db.Collection(usersCollection)),
	}
}

// EnsureIndexes creates user indexes. The unique account index is shared with room-service; failure messages guide the operator to the same fix.
func (r *UserRepo) EnsureIndexes(ctx context.Context) error {
	_, err := r.users.Raw().Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{{Key: "account", Value: 1}}, Options: options.Index().SetUnique(true),
	})
	if err == nil {
		return nil
	}
	// E11000: pre-existing duplicate accounts (populated env pre-rollout) — point operators at the one-time dedupe preflight.
	if mongo.IsDuplicateKeyError(err) {
		return fmt.Errorf("create user index: duplicate account values exist in the users collection — run the "+
			"one-time dedupe preflight (group users by account, resolve n>1) before starting this service: %w", err)
	}
	// A pre-existing non-unique account_1 conflicts (85 IndexOptionsConflict / 86 IndexKeySpecsConflict); Mongo won't upgrade it — the operator must drop it.
	if se := mongo.ServerError(nil); errors.As(err, &se) && (se.HasErrorCode(85) || se.HasErrorCode(86)) {
		return fmt.Errorf("create user index: a non-unique account_1 index already exists on the users collection — "+
			"drop the old non-unique account_1 index (db.users.dropIndex(\"account_1\")) before starting this service "+
			"so it can be recreated as unique: %w", err)
	}
	return fmt.Errorf("create user index: %w", err)
}

// activeUserFilter matches non-deactivated users. Missing `active` is treated as active ({$ne:false}); only explicit false excludes.
func activeUserFilter(account string) bson.M {
	return bson.M{"account": account, "active": bson.M{"$ne": false}}
}

// GetUserStatus returns the user for account (missing `active` counts as active),
// or (nil, nil). Projected to the StatusView fields; all others are zero-valued.
func (r *UserRepo) GetUserStatus(ctx context.Context, account string) (*model.User, error) {
	return r.users.FindOne(ctx, activeUserFilter(account),
		mongoutil.WithProjection(bson.M{
			"_id": 0, "account": 1, "statusText": 1, "statusIsShow": 1,
			"chineseName": 1, "engName": 1,
		}),
	)
}

// SetUserStatus updates status fields; isShow is only written when non-nil.
// Returns matched=false when no active user doc matched so callers can skip the cross-site broadcast.
func (r *UserRepo) SetUserStatus(ctx context.Context, account, text string, isShow *bool) (bool, error) {
	set := bson.M{"statusText": text}
	if isShow != nil {
		set["statusIsShow"] = *isShow
	}
	res, err := r.users.Raw().UpdateOne(ctx,
		activeUserFilter(account),
		bson.M{"$set": set},
	)
	if err != nil {
		return false, fmt.Errorf("update user status: %w", err)
	}
	return res.MatchedCount > 0, nil
}
