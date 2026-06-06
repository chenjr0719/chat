package verbs

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/hmchangw/chat/pkg/idgen"
)

// NATSRequest is the executor for the `nats_request` verb.
// It opens a connection per Execute call using the supplied credential,
// does one request/reply, and returns the outcome. Real production callers
// should pool connections, but for v2-Part1 a per-call connection is fine
// for correctness; pooling is a Part-2 optimization.
type NATSRequest struct {
	SiteURLs map[string]string
	Timeout  time.Duration
}

// NewNATSRequest returns a NATSRequest with sensible defaults.
func NewNATSRequest(siteURLs map[string]string) *NATSRequest {
	return &NATSRequest{SiteURLs: siteURLs, Timeout: 5 * time.Second}
}

func (n *NATSRequest) Execute(ctx context.Context, in *Input) Outcome {
	// Generated up-front so the Outcome carries it on every path
	// (success, transport error, missing credential) for correlation
	// against service logs. Per CLAUDE.md §3 every authoritative
	// handler in the system requires a valid hyphenated UUID
	// X-Request-ID header.
	requestID := idgen.GenerateRequestID()

	url, ok := n.SiteURLs[in.Site]
	if !ok || url == "" {
		return Outcome{
			Err:       fmt.Errorf("nats_request: no NATS URL for site %q", in.Site),
			RequestID: requestID,
		}
	}

	authOpt, err := credentialAuthOpt(in.Credential)
	if err != nil {
		return Outcome{
			Err:       fmt.Errorf("nats_request: url=%s: %w", url, err),
			RequestID: requestID,
		}
	}

	conn, err := nats.Connect(url,
		authOpt,
		nats.Name("integration-suite/"+in.Credential.Account),
	)
	if err != nil {
		return Outcome{
			Err:       fmt.Errorf("nats_request: connect: %w", err),
			RequestID: requestID,
		}
	}
	defer conn.Drain() //nolint:errcheck

	msg := nats.NewMsg(in.Subject)
	msg.Data = in.Payload
	msg.Header = nats.Header{}
	msg.Header.Set("X-Request-ID", requestID)
	if in.Traceparent != "" {
		msg.Header.Set("traceparent", in.Traceparent)
	}

	reqCtx, cancel := context.WithTimeout(ctx, n.Timeout)
	defer cancel()

	reply, err := conn.RequestMsgWithContext(reqCtx, msg)
	if err != nil {
		return Outcome{
			Err:             fmt.Errorf("nats_request: request: %w", err),
			TraceparentEcho: in.Traceparent,
			RequestID:       requestID,
		}
	}

	return Outcome{
		Reply:           reply.Data,
		Header:          reply.Header,
		TraceparentEcho: in.Traceparent,
		RequestID:       requestID,
	}
}

// Sentinel errors for transport-class classification by callers.
var (
	ErrNoResponders = errors.New("nats_request: no responders")
)
