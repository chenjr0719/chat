package mongorepo

import (
	"context"
	"fmt"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/mongoutil"
	"github.com/hmchangw/chat/user-service/models"
)

const appsCollection = "apps"

// AppRepo is the Mongo implementation of service.AppRepository.
type AppRepo struct {
	apps *mongoutil.Collection[model.App]
	// items views the same collection decoded as AppListItem ($addFields isSubscribed).
	items *mongoutil.Collection[models.AppListItem]
}

// NewAppRepo builds an AppRepo over db.
func NewAppRepo(db *mongo.Database) *AppRepo {
	col := db.Collection(appsCollection)
	return &AppRepo{
		apps:  mongoutil.NewCollection[model.App](col),
		items: mongoutil.NewCollection[models.AppListItem](col),
	}
}

// GetApp returns the app by id, or (nil, nil) when none matches.
func (r *AppRepo) GetApp(ctx context.Context, appID string) (*model.App, error) {
	return r.apps.FindByID(ctx, appID)
}

// ListApps returns a name-sorted page of apps with isSubscribed per user; Total is the full catalog count.
func (r *AppRepo) ListApps(ctx context.Context, account string, page mongoutil.OffsetPageRequest) (mongoutil.OffsetPage[models.AppListItem], error) {
	pipeline := bson.A{
		bson.M{"$lookup": bson.M{
			"from": subscriptionsCollection,
			"let":  bson.M{"botName": "$assistant.name"},
			"pipeline": bson.A{bson.M{"$match": bson.M{"$expr": bson.M{"$and": bson.A{
				// $literal so a $-prefixed account isn't read as a field path.
				bson.M{"$eq": bson.A{"$u.account", bson.M{"$literal": account}}},
				bson.M{"$eq": bson.A{"$name", "$$botName"}},
				bson.M{"$eq": bson.A{"$roomType", "botDM"}},
				bson.M{"$eq": bson.A{"$isSubscribed", true}},
			}}}}},
			"as": "sub",
		}},
		bson.M{"$addFields": bson.M{"isSubscribed": bson.M{"$gt": bson.A{bson.M{"$size": "$sub"}, 0}}}},
		bson.M{"$project": bson.M{"sub": 0}},
		bson.M{"$sort": bson.M{"name": 1}},
	}
	out, err := r.items.AggregatePaged(ctx, pipeline, page, mongoutil.WithAllowDiskUse())
	if err != nil {
		return mongoutil.OffsetPage[models.AppListItem]{}, fmt.Errorf("aggregate apps page: %w", err)
	}
	return out, nil
}

// GetAppNamesByAssistants maps bot account (assistant.name) → app display name for the given accounts.
func (r *AppRepo) GetAppNamesByAssistants(ctx context.Context, botAccounts []string) (map[string]string, error) {
	apps, err := r.apps.FindMany(ctx,
		bson.M{"assistant.name": bson.M{"$in": botAccounts}},
		mongoutil.WithProjection(bson.M{"_id": 0, "name": 1, "assistant.name": 1}),
	)
	if err != nil {
		return nil, fmt.Errorf("find apps by assistant names: %w", err)
	}
	out := make(map[string]string, len(apps))
	for i := range apps {
		if apps[i].Assistant != nil {
			out[apps[i].Assistant.Name] = apps[i].Name
		}
	}
	return out, nil
}
