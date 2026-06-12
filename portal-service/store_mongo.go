package main

import (
	"context"
	"errors"
	"fmt"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/hmchangw/chat/pkg/model"
)

type mongoDirectoryStore struct {
	users *mongo.Collection
	sites *mongo.Collection
}

func newMongoDirectoryStore(db *mongo.Database) *mongoDirectoryStore {
	return &mongoDirectoryStore{
		users: db.Collection("users"),
		sites: db.Collection("sites"),
	}
}

func (s *mongoDirectoryStore) FindUserByAccount(ctx context.Context, account string) (*model.User, error) {
	var u model.User
	err := s.users.FindOne(ctx,
		bson.M{"account": account},
		options.FindOne().SetProjection(bson.M{"_id": 1, "account": 1, "siteId": 1, "employeeId": 1}),
	).Decode(&u)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, fmt.Errorf("find user %q: %w", account, ErrUserNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("find user %q: %w", account, err)
	}
	return &u, nil
}

func (s *mongoDirectoryStore) FindSiteByID(ctx context.Context, siteID string) (*site, error) {
	var st site
	err := s.sites.FindOne(ctx, bson.M{"_id": siteID}).Decode(&st)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, fmt.Errorf("find site %q: %w", siteID, ErrSiteNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("find site %q: %w", siteID, err)
	}
	return &st, nil
}

// EnsureIndexes creates the unique account index; creation is idempotent for a matching spec.
func (s *mongoDirectoryStore) EnsureIndexes(ctx context.Context) error {
	_, err := s.users.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys:    bson.D{{Key: "account", Value: 1}},
		Options: options.Index().SetUnique(true),
	})
	if err != nil {
		return fmt.Errorf("ensure users (account) unique index: %w", err)
	}
	return nil
}
