// Command validator loads the catalogs/ directory and the scenarios/drafts/
// directory (both relative to the current working directory, or paths
// passed as positional arguments) and reports any reference drift or
// per-file scenario parse errors. Exits 0 on success, 1 on validation
// errors.
package main

import (
	"fmt"
	"os"

	cat "github.com/hmchangw/chat/tools/archived/integration-suite/internal/catalog"
	sc "github.com/hmchangw/chat/tools/archived/integration-suite/internal/scenario"
)

func main() {
	catRoot := "catalogs"
	scRoot := "scenarios/drafts"
	if len(os.Args) > 1 {
		catRoot = os.Args[1]
	}
	if len(os.Args) > 2 {
		scRoot = os.Args[2]
	}

	c, err := cat.Load(catRoot)
	if err != nil {
		fmt.Fprintf(os.Stderr, "load: %v\n", err)
		os.Exit(2)
	}

	// Universal primitives: six Poller types, one Verb type per
	// transport. Every backing reader the primitives consume
	// (NATSReplyReader, ContainerLogsReader, JetStreamSubjectReader,
	// NATSSubscribeReader) is opened by the universal poller from
	// per-args data at PollFn time — they aren't catalog-registered
	// by name any more.
	knownExecutors := map[string]bool{
		"NATSRequestExecutor":      true,
		"JetStreamPublishExecutor": true,
		"MongoFindPoller":          true,
		"CassandraSelectPoller":    true,
		"JetStreamConsumePoller":   true,
		"LogsTailPoller":           true,
		"NATSSubscribePoller":      true,
		"NATSReplyReader":          true, // backs the `reply` primitive (dispatcher-fed)
	}
	// Services are referenced from scenario YAML (args.container /
	// args.service / scenario.source) but the runner no longer
	// pre-binds pollers to them. knownServices stays empty — the
	// validator's reader-owner cross-check is vestigial when
	// owners: []. If a future catalog re-introduces owner
	// attribution, repopulate here.
	knownServices := map[string]bool{}

	catErrs := cat.Validate(c, knownExecutors, knownServices)
	if err := cat.ValidateForMishap(c); err != nil {
		catErrs = append(catErrs, err.Error())
	}
	if err := cat.ValidateReaderEvents(c); err != nil {
		catErrs = append(catErrs, err.Error())
	}
	loaded, scErrs := sc.LoadAllInDir(scRoot)

	if len(catErrs) > 0 || len(scErrs) > 0 {
		for _, e := range catErrs {
			fmt.Fprintln(os.Stderr, "ERROR:", e)
		}
		for _, e := range scErrs {
			fmt.Fprintln(os.Stderr, "ERROR:", e)
		}
		os.Exit(1)
	}
	fmt.Println("catalog: ok —", len(c.Verbs), "verbs,", len(c.Readers), "readers,", len(c.Services), "services")
	fmt.Println("scenarios: ok —", loaded, "drafts parsed")
}
