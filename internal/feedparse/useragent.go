package feedparse

import "os"

// UserAgent identifies nanoflux to the feeds and pages it fetches. A
// descriptive agent is good citizenship: some hosts (notably reddit) rate-limit
// or block opaque clients. Overridable with NF_USER_AGENT, e.g. to add a
// contact URL for a large instance.
func UserAgent() string {
	if v := os.Getenv("NF_USER_AGENT"); v != "" {
		return v
	}
	return "nanoflux (" + repoURL + ")"
}

// repoURL is where nanoflux lives, included in the default User-Agent so hosts
// can find out what is calling them.
const repoURL = "https://github.com/metruzanca/nanoflux"
