package main

import (
	"context"
	"errors"
	"fmt"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

type mongoProvisionStore struct {
	users *mongo.Collection
}

func newMongoProvisionStore(db *mongo.Database) *mongoProvisionStore {
	return &mongoProvisionStore{users: db.Collection("users")}
}

// EnsureIndexes mirrors room-service's users(account) index spec exactly, so
// either service can start first and the other's create is a no-op.
func (s *mongoProvisionStore) EnsureIndexes(ctx context.Context) error {
	if _, err := s.users.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{{Key: "account", Value: 1}},
	}); err != nil {
		return fmt.Errorf("ensure users (account) index: %w", err)
	}
	return nil
}

func (s *mongoProvisionStore) AccountProvisioned(ctx context.Context, account, siteID string) (bool, error) {
	err := s.users.FindOne(ctx,
		bson.M{"account": account, "siteId": siteID},
		options.FindOne().SetProjection(bson.M{"_id": 1}),
	).Err()
	if errors.Is(err, mongo.ErrNoDocuments) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("query users for provisioning: %w", err)
	}
	return true, nil
}
