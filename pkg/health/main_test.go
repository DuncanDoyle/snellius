package health

import (
	"os"
	"testing"
	"time"
)

// TestMain runs the tests in UTC, as CI does. Condition times decode into
// time.Local, so goldens would otherwise depend on the machine that wrote them.
func TestMain(m *testing.M) {
	time.Local = time.UTC
	os.Exit(m.Run())
}
