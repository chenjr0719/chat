// createoutbox is a tiny operator helper for `pre_fire_scripts`-hosted
// OUTBOX_<site> stream creation. The chat-app stream registry
// (pkg/stream.Outbox) declares the canonical shape; this binary
// applies it via the JetStream admin API.
//
// Why it exists: ARCHITECTURE.md §0 plus finding F-001 settle the
// "the tool does not create OUTBOX_<site>" rule (streams are ops/IaC
// territory). Scenarios that need OUTBOX present at fire-time wire
// `prep-outbox.sh` into `pre_fire_scripts:` as the operator-equivalent
// hook; `prep-outbox.sh` then runs THIS binary. The split keeps the
// `tool does not auto-fill ops/IaC gaps` discipline intact while
// giving authors a reproducible hook.
//
// Idempotent: uses CreateOrUpdateStream, so re-running on an already-
// created stream is a no-op.
//
// Usage (see scenarios/drafts/prep-outbox.sh):
//
//	go run tools/integration-suite-multisite/cmd/createoutbox \
//	    -url     "$ISM_SITE_A_NATS_URL"   \
//	    -creds   "$ISM_NATS_CREDS_FILE"   \
//	    -domain  site-a                   \
//	    -stream  OUTBOX_site-a            \
//	    -subject 'outbox.site-a.>'
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

func main() {
	os.Exit(run())
}

// run is the body of main, factored out so deferred cleanup
// (`nc.Drain`) actually executes before process exit. Returning
// non-zero from main directly via os.Exit would skip every defer.
func run() int {
	url := flag.String("url", "", "NATS URL (e.g., nats://localhost:4222) — required")
	creds := flag.String("creds", "", "absolute path to NATS credentials file — required")
	domain := flag.String("domain", "", "JetStream domain (e.g., site-a); empty = default domain")
	streamName := flag.String("stream", "", "stream name (e.g., OUTBOX_site-a) — required")
	subject := flag.String("subject", "", "subject pattern (e.g., outbox.site-a.>) — required")
	flag.Parse()

	if *url == "" || *creds == "" || *streamName == "" || *subject == "" {
		fmt.Fprintf(os.Stderr, "createoutbox: -url, -creds, -stream, -subject are all required\n")
		flag.Usage()
		return 2
	}

	nc, err := nats.Connect(
		*url,
		nats.UserCredentials(*creds),
		nats.Name("createoutbox"),
		nats.Timeout(5*time.Second),
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "createoutbox: NATS connect %s: %v\n", *url, err)
		return 1
	}
	defer func() { _ = nc.Drain() }()

	var js jetstream.JetStream
	if *domain != "" {
		js, err = jetstream.NewWithDomain(nc, *domain)
	} else {
		js, err = jetstream.New(nc)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "createoutbox: JetStream context (domain=%q): %v\n", *domain, err)
		return 1
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cfg := jetstream.StreamConfig{
		Name:     *streamName,
		Subjects: []string{*subject},
	}
	if _, err := js.CreateOrUpdateStream(ctx, cfg); err != nil {
		fmt.Fprintf(os.Stderr, "createoutbox: CreateOrUpdateStream %s (domain=%q, subject=%q): %v\n",
			*streamName, *domain, *subject, err)
		return 1
	}

	fmt.Printf("createoutbox: ready stream=%s domain=%s subject=%s\n", *streamName, *domain, *subject)
	return 0
}
