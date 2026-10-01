package poller

import (
	"os"
	"testing"
)

// TestMain lets tests reach httptest servers on loopback by opting out of the
// safedial guard for the whole test process.
func TestMain(m *testing.M) {
	os.Setenv("NF_ALLOW_PRIVATE_FETCH", "1")
	os.Exit(m.Run())
}
