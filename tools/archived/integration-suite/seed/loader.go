// Package seed provides Valkey room-key seeding for scenarios that
// publish canonical room events directly to JetStream (bypassing
// room-service's key-generation path). Those scenarios need a
// pre-existing Valkey key for the gate at
// room-worker/handler.go:1106-1114; LoadRoomKeys writes a fresh
// keypair for every roomID listed in room-keys.json.
//
// Other seed data is declared inline per scenario via seed.users[],
// seed.rooms[], seed.memberships[], and seed.cassandra_data — see
// SCENARIO-REFERENCE.md §2.4.
package seed

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"

	"github.com/hmchangw/chat/pkg/roomkeystore"
)

//go:embed room-keys.json
var roomKeysJSON []byte

// DecodedRoomKeys returns the list of room IDs that should have a
// fresh Valkey room key seeded at suite startup.
func DecodedRoomKeys() ([]string, error) {
	var ids []string
	if err := json.Unmarshal(roomKeysJSON, &ids); err != nil {
		return nil, fmt.Errorf("decode room-keys.json: %w", err)
	}
	return ids, nil
}

// LoadRoomKeys generates a fresh keypair for every room ID in
// room-keys.json and writes them to the supplied Valkey-backed store.
// Idempotent: re-running overwrites the existing keys, which is fine
// because pure room-worker scenarios never read the key's contents —
// only its presence. Intended to be called once at suite startup.
func LoadRoomKeys(ctx context.Context, store roomkeystore.RoomKeyStore) error {
	ids, err := DecodedRoomKeys()
	if err != nil {
		return err
	}
	for _, id := range ids {
		pair, err := roomkeystore.GenerateKeyPair()
		if err != nil {
			return fmt.Errorf("seed room key %s: generate: %w", id, err)
		}
		if _, err := store.Set(ctx, id, *pair); err != nil {
			return fmt.Errorf("seed room key %s: set: %w", id, err)
		}
	}
	return nil
}
