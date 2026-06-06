package scenario

import "fmt"

// knownSites is the closed list of valid site names. Multi-site is
// always exactly 2 sites — see spec §1 decision row 2.
var knownSites = map[string]struct{}{
	"site-a": {},
	"site-b": {},
}

// requiresSite returns true for poller locations that need a site
// arg. mongo_find, jetstream_consume, nats_subscribe, logs_tail.
func requiresSite(location string) bool {
	switch location {
	case "mongo_find", "jetstream_consume", "nats_subscribe", "logs_tail":
		return true
	}
	return false
}

// forbidsSite returns true for poller locations that MUST NOT carry
// a site arg. reply (intrinsic to the fire) and cassandra_select
// (shared cluster).
func forbidsSite(location string) bool {
	switch location {
	case "reply", "cassandra_select":
		return true
	}
	return false
}

// ValidateSiteFields checks the rules in spec §3.2:
//   - input.site is required and must be one of knownSites.
//   - expected[i].site is required when location is site-scoped,
//     forbidden when location is reply or cassandra_select, and
//     when present must be one of knownSites.
func ValidateSiteFields(s *Scenario) error {
	if s.Input.Site == "" {
		return fmt.Errorf("scenario %q: input.site is required (one of site-a, site-b)", s.Name)
	}
	if _, ok := knownSites[s.Input.Site]; !ok {
		return fmt.Errorf("scenario %q: input.site = %q; must be site-a or site-b", s.Name, s.Input.Site)
	}
	for i, e := range s.Expected {
		switch {
		case requiresSite(e.Location):
			if e.Site == "" {
				return fmt.Errorf("scenario %q: expected[%d] location=%s requires a site field", s.Name, i, e.Location)
			}
			if _, ok := knownSites[e.Site]; !ok {
				return fmt.Errorf("scenario %q: expected[%d].site = %q; must be site-a or site-b", s.Name, i, e.Site)
			}
		case forbidsSite(e.Location):
			if e.Site != "" {
				return fmt.Errorf("scenario %q: expected[%d] location=%s must not carry a site field", s.Name, i, e.Location)
			}
		default:
			// Unknown location — let the registry catch it at run time.
		}
	}
	return nil
}
