package runtime

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/hmchangw/chat/tools/integration-suite-multisite/internal/scenario"
)

// TestSubstituteMongoDocTokens_SimpleAliasField covers the common
// case: a top-level string value carrying ${alice.id} resolves to
// the user's materialized ID.
func TestSubstituteMongoDocTokens_SimpleAliasField(t *testing.T) {
	ctx := Context{
		Placeholders: map[string]map[string]any{
			"alice": {
				"id":      "u-alice-real",
				"account": "alice",
			},
		},
	}
	doc := map[string]any{
		"_id":     "tr-1",
		"ownerId": "${alice.id}",
		"account": "${alice.account}",
	}

	require.NoError(t, substituteMongoDocTokens(doc, ctx))

	assert.Equal(t, "u-alice-real", doc["ownerId"])
	assert.Equal(t, "alice", doc["account"])
	assert.Equal(t, "tr-1", doc["_id"], "literal strings must not be touched")
}

// TestSubstituteMongoDocTokens_NowTokenSkipped: the now-token pass
// runs separately so the substitute pass must leave ${now ± d} alone.
func TestSubstituteMongoDocTokens_NowTokenSkipped(t *testing.T) {
	ctx := Context{Placeholders: map[string]map[string]any{}}
	doc := map[string]any{
		"createdAt": "${now - 1h}",
	}

	require.NoError(t, substituteMongoDocTokens(doc, ctx))

	assert.Equal(t, "${now - 1h}", doc["createdAt"],
		"now-tokens must pass through substituteMongoDocTokens unchanged")
}

// TestSubstituteMongoDocTokens_RecursesIntoNestedMap exercises the
// nested-map shape — e.g. thread_rooms.parent (a sub-document with
// its own ${alice.id} reference).
func TestSubstituteMongoDocTokens_RecursesIntoNestedMap(t *testing.T) {
	ctx := Context{
		Placeholders: map[string]map[string]any{
			"alice": {"id": "u-alice-real"},
		},
	}
	doc := map[string]any{
		"parent": map[string]any{
			"messageId": "m-abc",
			"senderId":  "${alice.id}",
		},
	}

	require.NoError(t, substituteMongoDocTokens(doc, ctx))

	inner := doc["parent"].(map[string]any)
	assert.Equal(t, "u-alice-real", inner["senderId"])
	assert.Equal(t, "m-abc", inner["messageId"])
}

// TestSubstituteMongoDocTokens_RecursesIntoList exercises array
// shapes — e.g. replyAccounts (a list of account strings, some of
// which are alias-tokens).
func TestSubstituteMongoDocTokens_RecursesIntoList(t *testing.T) {
	ctx := Context{
		Placeholders: map[string]map[string]any{
			"alice": {"account": "alice"},
			"bob":   {"account": "bob"},
		},
	}
	doc := map[string]any{
		"replyAccounts": []any{"${alice.account}", "${bob.account}", "carol"},
	}

	require.NoError(t, substituteMongoDocTokens(doc, ctx))

	got := doc["replyAccounts"].([]any)
	require.Len(t, got, 3)
	assert.Equal(t, "alice", got[0])
	assert.Equal(t, "bob", got[1])
	assert.Equal(t, "carol", got[2])
}

// TestResolveMongoDocNowTokens_TopLevel: a literal ${now - 5m} at
// the top level resolves to a time.Time anchored at tOpen minus 5m.
func TestResolveMongoDocNowTokens_TopLevel(t *testing.T) {
	tOpen := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	doc := map[string]any{
		"createdAt": "${now - 5m}",
	}

	require.NoError(t, resolveMongoDocNowTokens(doc, tOpen))

	require.IsType(t, time.Time{}, doc["createdAt"])
	got := doc["createdAt"].(time.Time)
	assert.True(t, tOpen.Sub(got) == 5*time.Minute,
		"createdAt must be exactly 5m before tOpen, got delta = %v", tOpen.Sub(got))
}

// TestResolveMongoDocNowTokens_RecursesIntoNestedMap: a now-token
// inside a sub-document also resolves (e.g. a thread parent's
// createdAt embedded in a thread_rooms doc).
func TestResolveMongoDocNowTokens_RecursesIntoNestedMap(t *testing.T) {
	tOpen := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	doc := map[string]any{
		"parent": map[string]any{
			"createdAt": "${now - 1h}",
		},
	}

	require.NoError(t, resolveMongoDocNowTokens(doc, tOpen))

	inner := doc["parent"].(map[string]any)
	require.IsType(t, time.Time{}, inner["createdAt"])
	assert.Equal(t, time.Hour, tOpen.Sub(inner["createdAt"].(time.Time)))
}

// TestResolveMongoDocNowTokens_NonStringValueUntouched: scalars and
// non-now strings pass through.
func TestResolveMongoDocNowTokens_NonStringValueUntouched(t *testing.T) {
	tOpen := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	when := time.Date(2026, 6, 1, 11, 0, 0, 0, time.UTC)
	doc := map[string]any{
		"messageId": "m-abc",
		"count":     5,
		"createdAt": when,
	}

	require.NoError(t, resolveMongoDocNowTokens(doc, tOpen))

	assert.Equal(t, "m-abc", doc["messageId"])
	assert.Equal(t, 5, doc["count"])
	assert.Equal(t, when, doc["createdAt"])
}

// TestValidateMongoDataEntry_HappyPath covers the canonical accepted
// shape: known site + allowed collection + live db handle.
func TestValidateMongoDataEntry_HappyPath(t *testing.T) {
	entry := scenario.SeedMongoCollection{
		Site:       "site-a",
		Collection: "thread_rooms",
	}
	mongoBySite := map[string]*mongo.Database{
		"site-a": {},
	}

	err := validateMongoDataEntry(0, entry, mongoBySite)
	assert.NoError(t, err)
}

// TestValidateMongoDataEntry_RejectsEmptySite: the loader doesn't
// require site, so the runtime gate must.
func TestValidateMongoDataEntry_RejectsEmptySite(t *testing.T) {
	entry := scenario.SeedMongoCollection{Collection: "thread_rooms"}

	err := validateMongoDataEntry(0, entry, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "site is required")
}

// TestValidateMongoDataEntry_RejectsEmptyCollection: same reason.
func TestValidateMongoDataEntry_RejectsEmptyCollection(t *testing.T) {
	entry := scenario.SeedMongoCollection{Site: "site-a"}

	err := validateMongoDataEntry(0, entry, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "collection is required")
}

// TestValidateMongoDataEntry_RejectsUnknownCollection covers the
// closed-catalog discipline: a scenario can't seed an arbitrary
// collection (the sandbox wouldn't be able to drop it between runs
// to restore byte-identical state).
func TestValidateMongoDataEntry_RejectsUnknownCollection(t *testing.T) {
	entry := scenario.SeedMongoCollection{
		Site:       "site-a",
		Collection: "totally-unknown-collection",
	}
	mongoBySite := map[string]*mongo.Database{"site-a": {}}

	err := validateMongoDataEntry(0, entry, mongoBySite)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not in the sandbox-owned set")
	assert.Contains(t, err.Error(), "thread_rooms",
		"error message must include the allowed-list for author guidance")
}

// TestValidateMongoDataEntry_RejectsUnknownSite: the site name must
// match a live Mongo handle in this run. Catches the typo case where
// an author writes site: site-c in a 2-site stack.
func TestValidateMongoDataEntry_RejectsUnknownSite(t *testing.T) {
	entry := scenario.SeedMongoCollection{
		Site:       "site-c",
		Collection: "thread_rooms",
	}
	mongoBySite := map[string]*mongo.Database{"site-a": {}}

	err := validateMongoDataEntry(0, entry, mongoBySite)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no live Mongo handle")
}

// TestCopyMongoDoc_IsShallow: the binder mutates its copy during
// substitution; the parsed Scenario must keep its original token
// strings so re-Setup (defensive — not currently in the runner
// loop) still resolves correctly.
func TestCopyMongoDoc_IsShallow(t *testing.T) {
	original := map[string]any{
		"_id":       "tr-1",
		"createdAt": "${now - 5m}",
	}

	copy := copyMongoDoc(original)
	copy["createdAt"] = time.Now() // simulate resolution

	assert.Equal(t, "${now - 5m}", original["createdAt"],
		"copy must not share top-level entries with the source")
}
