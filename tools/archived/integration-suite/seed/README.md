# Seed data for integration-suite

This directory holds one file:

| File | Loaded by | Purpose |
|------|-----------|---------|
| `room-keys.json` | `seed.LoadRoomKeys` | List of `roomId` strings that need a pre-seeded Valkey keypair at suite startup. |

## When you need this

Pure-room-worker scenarios publish synthetic canonical room events
directly to JetStream (bypassing room-service's key-generation path).
`room-worker/handler.go:1106-1114` gates on the Valkey room key
existing — without it, the worker's `process message` step fails
with `room key absent for <roomID>`.

If your scenario publishes to `chat.room.canonical.${site}.*` with a
literal `roomId`, add that `roomId` to `room-keys.json` so the runner
pre-seeds a Valkey key for it at startup.

If your scenario uses `nats_request` (room creation through
room-service), this file does not apply — room-service mints the key
itself.

## Seeding

Set `VALKEY_ADDR` before running the suite:

```bash
VALKEY_ADDR=localhost:6379 make -C tools/integration-suite local
```

`USE_INFRA=true` populates `VALKEY_ADDR` automatically from the
boot-time Valkey cluster.

When `VALKEY_ADDR` is unset the runner skips this seeding and warns
at startup. Scenarios that depended on a pre-seeded key will fail
their pure-room-worker assertion when room-worker rejects the event
for missing-key.

## Naming

- Room IDs are arbitrary strings; convention is `r-<slug>`
  (e.g. `r-pure-test`) but the runner doesn't enforce it.
