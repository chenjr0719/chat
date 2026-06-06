package readers

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hmchangw/chat/tools/integration-suite-multisite/internal/verbs"
)

// TestNewReplyPayload_JSONSuccess covers the happy path: reply bytes
// decode as a JSON object; BodyJSON populated; Error empty.
func TestNewReplyPayload_JSONSuccess(t *testing.T) {
	out := verbs.Outcome{
		Reply:     []byte(`{"status":"accepted","roomType":"channel","roomId":"r-abc"}`),
		Header:    nats.Header{"X-Request-Id": []string{"01970000-0000-7000-8000-000000000001"}},
		RequestID: "01970000-0000-7000-8000-000000000001",
	}
	p := NewReplyPayload(&out, 42*time.Millisecond)
	require.Empty(t, p.Error)
	require.Empty(t, p.BodyRaw)
	require.NotNil(t, p.BodyJSON)
	assert.Equal(t, "accepted", p.BodyJSON["status"])
	assert.Equal(t, "channel", p.BodyJSON["roomType"])
	assert.Equal(t, []string{"01970000-0000-7000-8000-000000000001"}, p.Header["X-Request-Id"])
	assert.Equal(t, int64(42), p.LatencyMs)
}

// TestNewReplyPayload_NonJSONSuccess covers a successful reply whose
// body is not JSON-decodable — BodyRaw carries the verbatim string.
func TestNewReplyPayload_NonJSONSuccess(t *testing.T) {
	out := verbs.Outcome{
		Reply:  []byte("OK"),
		Header: nats.Header{"Content-Type": []string{"text/plain"}},
	}
	p := NewReplyPayload(&out, 1*time.Millisecond)
	require.Empty(t, p.Error)
	require.Nil(t, p.BodyJSON)
	assert.Equal(t, "OK", p.BodyRaw)
}

// TestNewReplyPayload_TransportError covers no-responders / timeout:
// Reply is empty, Err is set, Error string is verbatim.
func TestNewReplyPayload_TransportError(t *testing.T) {
	out := verbs.Outcome{
		Err: errors.New("nats_request: request: nats: no responders available for request"),
	}
	p := NewReplyPayload(&out, 5*time.Second)
	require.NotEmpty(t, p.Error)
	assert.Contains(t, p.Error, "no responders")
	assert.Nil(t, p.BodyJSON)
	assert.Empty(t, p.BodyRaw)
	assert.Empty(t, p.Header)
	assert.Equal(t, int64(5000), p.LatencyMs)
}

// TestNATSReplyReader_InjectEmitsEvent confirms the reader plumbs the
// typed payload through Watch.
func TestNATSReplyReader_InjectEmitsEvent(t *testing.T) {
	r := NewNATSReplyReader()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch, err := r.Watch(ctx, "run-id", time.Now())
	require.NoError(t, err)

	r.Inject(
		&verbs.Outcome{Reply: []byte(`{"k":"v"}`)},
		10*time.Millisecond,
		"00-trace-span-01",
		time.Now(),
		"",
	)

	select {
	case ev := <-ch:
		assert.Equal(t, "reply", ev.Location)
		assert.Equal(t, "00-trace-span-01", ev.Traceparent)
		payload, ok := ev.Payload.(ReplyPayload)
		require.True(t, ok, "Event.Payload should be ReplyPayload, got %T", ev.Payload)
		assert.Equal(t, "v", payload.BodyJSON["k"])
		assert.Equal(t, int64(10), payload.LatencyMs)
	case <-time.After(500 * time.Millisecond):
		t.Fatal("did not receive injected event within 500ms")
	}
}
