package catalog

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/hmchangw/chat/tools/integration-suite-multisite/internal/readers"
)

func TestValidate_AllReferenced(t *testing.T) {
	c := &Catalog{
		Verbs:    []Verb{{Name: "nats_request", Executor: "NATSRequestExecutor"}},
		Readers:  []Reader{{Location: "mongo.rooms", Owners: []string{"room-service"}, Executor: "MongoRoomsReader"}},
		Services: []Service{{Name: "room-service"}},
	}
	knownExecutors := map[string]bool{
		"NATSRequestExecutor": true,
		"MongoRoomsReader":    true,
	}
	knownServices := map[string]bool{"room-service": true}

	errs := Validate(c, knownExecutors, knownServices)
	assert.Empty(t, errs)
}

func TestValidate_UnknownExecutor(t *testing.T) {
	c := &Catalog{
		Verbs: []Verb{{Name: "nats_request", Executor: "NonexistentExecutor"}},
	}
	errs := Validate(c, map[string]bool{}, map[string]bool{})
	assert.Len(t, errs, 1)
	assert.Contains(t, errs[0], "NonexistentExecutor")
}

func TestValidate_ReaderOwnerNotAService(t *testing.T) {
	c := &Catalog{
		Readers:  []Reader{{Location: "mongo.rooms", Owners: []string{"not-a-service"}, Executor: "X"}},
		Services: []Service{{Name: "room-service"}},
	}
	knownExecutors := map[string]bool{"X": true}
	knownServices := map[string]bool{"room-service": true}
	errs := Validate(c, knownExecutors, knownServices)
	assert.NotEmpty(t, errs)
	assert.Contains(t, errs[0], "not-a-service")
}

func TestValidator_ServiceMissingContainerFails(t *testing.T) {
	cat := &Catalog{Services: []Service{
		{Name: "room-worker", Container: ""},
	}}
	err := ValidateForMishap(cat)
	assert.ErrorContains(t, err, "room-worker")
	assert.ErrorContains(t, err, "container")
}

func TestValidator_ServiceWithContainerPasses(t *testing.T) {
	cat := &Catalog{Services: []Service{
		{Name: "room-worker", Container: "room-worker"},
	}}
	err := ValidateForMishap(cat)
	assert.NoError(t, err)
}

func TestValidator_AccumulatesMultipleContainerErrors(t *testing.T) {
	cat := &Catalog{Services: []Service{
		{Name: "svc-a", Container: ""},
		{Name: "svc-b", Container: "ok"},
		{Name: "svc-c", Container: "  "},
	}}
	err := ValidateForMishap(cat)
	assert.Error(t, err)
	msg := err.Error()
	assert.Contains(t, msg, "svc-a")
	assert.Contains(t, msg, "svc-c")
	assert.NotContains(t, msg, "svc-b")
}

func TestValidator_ReaderEventMissingTypeFails(t *testing.T) {
	cat := &Catalog{Readers: []Reader{
		{
			Location: "logs.room-worker",
			Events:   []ReaderEvent{{Pattern: "^level=info", Type: ""}},
		},
	}}
	err := ValidateReaderEvents(cat)
	assert.ErrorContains(t, err, "logs.room-worker")
	assert.ErrorContains(t, err, "type")
}

func TestValidator_ReaderEventUnknownTypeFails(t *testing.T) {
	cat := &Catalog{Readers: []Reader{
		{
			Location: "logs.room-worker",
			Events:   []ReaderEvent{{Pattern: "x", Type: readers.EventType("weird")}},
		},
	}}
	err := ValidateReaderEvents(cat)
	assert.ErrorContains(t, err, "weird")
}

func TestValidator_ReaderEventValidTypesPass(t *testing.T) {
	cat := &Catalog{Readers: []Reader{
		{
			Location: "logs.room-worker",
			Events: []ReaderEvent{
				{Pattern: "a", Type: readers.EventCascade},
				{Pattern: "b", Type: readers.EventFailure},
				{Pattern: "c", Type: readers.EventRestartNoise},
				{Pattern: "d", Type: readers.EventDisconnectNoise},
				{Pattern: "e", Type: readers.EventBackground},
			},
		},
	}}
	err := ValidateReaderEvents(cat)
	assert.NoError(t, err)
}
