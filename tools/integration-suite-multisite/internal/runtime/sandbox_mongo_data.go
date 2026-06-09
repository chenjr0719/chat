package runtime

import (
	"context"
	"fmt"
	"sort"
	"time"

	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/hmchangw/chat/tools/integration-suite-multisite/internal/scenario"
)

// mongoDataAllowedCollections is the closed set of Mongo collections
// the sandbox manages. A scenario's mongo_data entry MUST reference
// one of these — the loader rejects anything else at validate time
// so authors don't accidentally pre-populate a collection the
// sandbox can't reset between runs. Mirrors the chat-app's actual
// write-target set so production parity is preserved.
//
// Today the union of sandboxOwnedCollections (dropped at Setup) and
// this set is the same — both expanded for the §2.7 T3 work. Kept
// as two consts for clarity: sandboxOwnedCollections names the
// drop contract; mongoDataAllowedCollections names the seed
// contract. Authors don't think about drops; they think about what
// they can write.
var mongoDataAllowedCollections = map[string]struct{}{
	"users":                {},
	"rooms":                {},
	"subscriptions":        {},
	"room_members":         {},
	"thread_rooms":         {},
	"thread_subscriptions": {},
}

// insertSeededMongoData materializes sb.Scenario.MongoData into the
// per-site Mongo handles. Substitution + ${now ± d} resolution
// matches the cassandra_data pipeline shape.
//
// Caller (Sandbox.Setup) guarantees sb.Deps.MongoBySite is non-nil
// for every site referenced by mongo_data. Loader rejects unknown
// sites and collections before this runs.
func insertSeededMongoData(ctx context.Context, sb *Sandbox) error {
	return runInsertMongoSeed(ctx, sb)
}

// runInsertMongoSeed is the testable orchestration core. Unit tests
// inject sb with a stubbed MongoBySite; production wraps live
// *mongo.Database handles. Per-doc substitution + now-token
// resolution runs before the InsertOne so the bound value is what
// the chat-app code expects to read.
func runInsertMongoSeed(ctx context.Context, sb *Sandbox) error {
	if len(sb.Scenario.MongoData) == 0 {
		return nil
	}

	subCtx := Context{
		Placeholders: sb.Placeholders,
		Services:     sb.Deps.Services,
	}

	for entryIdx, entry := range sb.Scenario.MongoData {
		if err := validateMongoDataEntry(entryIdx, entry, sb.Deps.MongoBySite); err != nil {
			return err
		}
		col := sb.Deps.MongoBySite[entry.Site].Collection(entry.Collection)

		for docIdx, src := range entry.Docs {
			doc := copyMongoDoc(src)

			if err := substituteMongoDocTokens(doc, subCtx); err != nil {
				return fmt.Errorf("mongo_data[%d (%s)][%d]: %w", entryIdx, entry.Collection, docIdx, err)
			}
			if err := resolveMongoDocNowTokens(doc, sb.StartTime); err != nil {
				return fmt.Errorf("mongo_data[%s][%d]: %w", entry.Collection, docIdx, err)
			}

			if _, err := col.InsertOne(ctx, doc); err != nil {
				return fmt.Errorf("mongo_data[%s][%d]: insert: %w", entry.Collection, docIdx, err)
			}
		}
	}
	return nil
}

// validateMongoDataEntry checks site / collection / docs invariants
// before any I/O. The loader has its own static checks; this is the
// runtime guard for the cases the loader can't catch alone
// (specifically: that the site referenced in mongo_data actually has
// a live Mongo handle in this run — depends on which sites the
// scenario declared).
func validateMongoDataEntry(idx int, entry scenario.SeedMongoCollection, mongoBySite map[string]*mongo.Database) error {
	if entry.Site == "" {
		return fmt.Errorf("mongo_data[%d]: site is required", idx)
	}
	if entry.Collection == "" {
		return fmt.Errorf("mongo_data[%d]: collection is required", idx)
	}
	if _, ok := mongoDataAllowedCollections[entry.Collection]; !ok {
		return fmt.Errorf("mongo_data[%d]: collection %q is not in the sandbox-owned set; allowed: %v",
			idx, entry.Collection, sortedAllowedCollections())
	}
	db, ok := mongoBySite[entry.Site]
	if !ok || db == nil {
		return fmt.Errorf("mongo_data[%d]: site %q has no live Mongo handle in this run", idx, entry.Site)
	}
	return nil
}

// sortedAllowedCollections returns the allowed-collection set as a
// deterministic, sorted slice for inclusion in error messages.
// Authors hitting the loader error see a stable list.
func sortedAllowedCollections() []string {
	out := make([]string, 0, len(mongoDataAllowedCollections))
	for c := range mongoDataAllowedCollections {
		out = append(out, c)
	}
	sort.Strings(out)
	return out
}

// substituteMongoDocTokens resolves ${alice.id} / ${site} / etc.
// tokens in string values across the doc. Skips ${now ± d} tokens
// (handled by the next pass, like cassandra_data — same rationale
// in sandbox_cassandra.go's substituteCassandraRowTokens comment).
//
// Recurses into nested map[string]any (e.g. UDT-shaped Mongo sub-
// documents) and []any (lists of values). Sorted-key iteration for
// deterministic error ordering.
func substituteMongoDocTokens(doc map[string]any, ctx Context) error {
	for _, key := range sortedMongoDocKeys(doc) {
		switch v := doc[key].(type) {
		case string:
			if scenario.IsNowToken(v) {
				continue
			}
			resolved, err := Substitute(v, ctx)
			if err != nil {
				return fmt.Errorf("field %q: %w", key, err)
			}
			doc[key] = resolved
		case map[string]any:
			if err := substituteMongoDocTokens(v, ctx); err != nil {
				return fmt.Errorf("field %q: %w", key, err)
			}
		case []any:
			for i := range v {
				if inner, ok := v[i].(map[string]any); ok {
					if err := substituteMongoDocTokens(inner, ctx); err != nil {
						return fmt.Errorf("field %q[%d]: %w", key, i, err)
					}
					continue
				}
				if s, ok := v[i].(string); ok && !scenario.IsNowToken(s) {
					resolved, err := Substitute(s, ctx)
					if err != nil {
						return fmt.Errorf("field %q[%d]: %w", key, i, err)
					}
					v[i] = resolved
				}
			}
		}
	}
	return nil
}

// resolveMongoDocNowTokens converts ${now ± duration} tokens to
// time.Time anchored at sb.StartTime (T_open). Mirrors
// scenario.ResolveRowNowTokens but for the plain map[string]any
// shape the Mongo seed pipeline uses. Recurses into nested maps and
// lists so a ${now} embedded inside a sub-document also resolves.
func resolveMongoDocNowTokens(doc map[string]any, tOpen time.Time) error {
	for _, key := range sortedMongoDocKeys(doc) {
		switch v := doc[key].(type) {
		case string:
			if !scenario.IsNowToken(v) {
				continue
			}
			resolved, err := scenario.ResolveCreatedAtToken(v, tOpen)
			if err != nil {
				return fmt.Errorf("field %q: %w", key, err)
			}
			doc[key] = resolved
		case map[string]any:
			if err := resolveMongoDocNowTokens(v, tOpen); err != nil {
				return fmt.Errorf("field %q: %w", key, err)
			}
		case []any:
			for i := range v {
				if inner, ok := v[i].(map[string]any); ok {
					if err := resolveMongoDocNowTokens(inner, tOpen); err != nil {
						return fmt.Errorf("field %q[%d]: %w", key, i, err)
					}
				}
			}
		}
	}
	return nil
}

// sortedMongoDocKeys returns the doc's keys in lexical order for
// deterministic error coordinates.
func sortedMongoDocKeys(doc map[string]any) []string {
	out := make([]string, 0, len(doc))
	for k := range doc {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// copyMongoDoc returns a shallow copy of the doc so the substitution
// passes don't mutate the parsed Scenario's seed block. Matches the
// defensive copy discipline in sandbox_cassandra.go's
// copyCassandraRow.
func copyMongoDoc(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
