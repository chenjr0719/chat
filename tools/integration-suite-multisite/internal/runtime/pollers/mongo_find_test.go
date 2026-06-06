package pollers

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

func TestMongoFindPoller_NilSiteIsWarnNotPanic(t *testing.T) {
	// Sanity: an empty Sites map should produce a poller whose
	// PollFn returns nil events without panicking. (Real backend
	// tests live in an integration test under -tags integration.)
	p := &MongoFindPoller{Sites: nil}
	pollFn := p.PollFn("site-a", map[string]any{
		"collection": "rooms",
		"filter":     map[string]any{},
	}, "")
	events := pollFn()
	assert.Empty(t, events)
}

func TestMongoFindPoller_UnknownSiteIsWarnNotPanic(t *testing.T) {
	p := &MongoFindPoller{
		Sites: map[string]*mongo.Database{}, // empty but non-nil
	}
	pollFn := p.PollFn("site-z", map[string]any{
		"collection": "rooms",
	}, "")
	events := pollFn()
	assert.Empty(t, events)
}
