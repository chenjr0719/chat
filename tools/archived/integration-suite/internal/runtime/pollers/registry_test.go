package pollers

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hmchangw/chat/tools/archived/integration-suite/internal/readers"
)

// fakePoller is a test double whose PollFn returns a canned slice.
type fakePoller struct {
	events []readers.Event
}

func (f *fakePoller) PollFn(_ map[string]any, _ string) func() []readers.Event {
	return func() []readers.Event { return f.events }
}

func TestNewRegistry_IsEmpty(t *testing.T) {
	r := NewRegistry()
	_, err := r.Get("reply")
	require.Error(t, err)
}

func TestRegistry_RegisterAndGet(t *testing.T) {
	r := NewRegistry()
	want := []readers.Event{{Location: "reply", OwnerSvc: "room-service"}}
	r.Register("reply", &fakePoller{events: want})

	got, err := r.Get("reply")
	require.NoError(t, err)
	assert.NotNil(t, got)
	assert.Equal(t, want, got.PollFn(nil, "")())
}

func TestRegistry_UnknownLocationNamesAvailableSet(t *testing.T) {
	r := NewRegistry()
	r.Register("reply", &fakePoller{})
	r.Register("mongo.rooms", &fakePoller{})

	_, err := r.Get("logs.room-service")
	require.Error(t, err)
	msg := err.Error()
	assert.Contains(t, msg, "logs.room-service")
	assert.Contains(t, msg, "reply", "error must list available")
	assert.Contains(t, msg, "mongo.rooms", "error must list available")
}

func TestRegistry_LocationsReturnsSortedNames(t *testing.T) {
	r := NewRegistry()
	r.Register("mongo.rooms", &fakePoller{})
	r.Register("reply", &fakePoller{})
	r.Register("logs.room-service", &fakePoller{})

	assert.Equal(t, []string{"logs.room-service", "mongo.rooms", "reply"}, r.Locations())
}
