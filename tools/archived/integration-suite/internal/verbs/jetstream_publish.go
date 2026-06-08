package verbs

import (
	"context"
	"fmt"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/hmchangw/chat/pkg/idgen"
)

// JetStreamPublish is the executor for the `jetstream_publish` verb.
// Opens a connection per Execute call using the supplied credential,
// gets a JetStream context, and publishes one message. Per-call
// connection (matching NATSRequest's discipline); pooling is a
// Part-2 optimization. Returns the ack as a JSON {"stream":...,
// "sequence":...} blob in Outcome.Reply so scenarios can assert on it.
type JetStreamPublish struct {
	NATSURL string
	Timeout time.Duration
}

// NewJetStreamPublish returns a JetStreamPublish with sensible defaults.
func NewJetStreamPublish(natsURL string) *JetStreamPublish {
	return &JetStreamPublish{NATSURL: natsURL, Timeout: 5 * time.Second}
}

func (n *JetStreamPublish) Execute(ctx context.Context, in *Input) Outcome {
	// Generated up-front so the Outcome carries it on every path
	// (success, transport error, missing credential) for correlation
	// against service logs. Same X-Request-ID discipline as NATSRequest.
	requestID := idgen.GenerateRequestID()

	authOpt, err := credentialAuthOpt(in.Credential)
	if err != nil {
		return Outcome{
			Err:       fmt.Errorf("jetstream_publish: %w", err),
			RequestID: requestID,
		}
	}

	conn, err := nats.Connect(n.NATSURL,
		authOpt,
		nats.Name("integration-suite/"+in.Credential.Account),
	)
	if err != nil {
		return Outcome{
			Err:       fmt.Errorf("jetstream_publish: connect: %w", err),
			RequestID: requestID,
		}
	}
	defer conn.Drain() //nolint:errcheck

	js, err := jetstream.New(conn)
	if err != nil {
		return Outcome{
			Err:       fmt.Errorf("jetstream_publish: jetstream: %w", err),
			RequestID: requestID,
		}
	}

	msg := nats.NewMsg(in.Subject)
	msg.Data = in.Payload
	msg.Header = nats.Header{}
	msg.Header.Set("X-Request-ID", requestID)
	if in.Traceparent != "" {
		msg.Header.Set("traceparent", in.Traceparent)
	}

	pubCtx, cancel := context.WithTimeout(ctx, n.Timeout)
	defer cancel()

	ack, err := js.PublishMsg(pubCtx, msg)
	if err != nil {
		return Outcome{
			Err:             fmt.Errorf("jetstream_publish: publish: %w", err),
			TraceparentEcho: in.Traceparent,
			RequestID:       requestID,
		}
	}

	ackJSON := []byte(fmt.Sprintf(`{"stream":%q,"sequence":%d}`, ack.Stream, ack.Sequence))
	return Outcome{
		Reply:           ackJSON,
		TraceparentEcho: in.Traceparent,
		RequestID:       requestID,
	}
}
