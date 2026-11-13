package realtimetest_test

import (
	"testing"

	"github.com/go-tenon/tenon/realtime"
	"github.com/go-tenon/tenon/realtime/realtimetest"
)

func TestPresence(t *testing.T) {
	realtimetest.TestPresenceStore(t, func(*testing.T) realtime.PresenceStore { return realtimetest.NewPresence() })
}
