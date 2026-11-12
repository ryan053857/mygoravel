package realtimetest_test

import (
	"testing"

	"github.com/ryan053857/mygoravel/realtime"
	"github.com/ryan053857/mygoravel/realtime/realtimetest"
)

func TestPresence(t *testing.T) {
	realtimetest.TestPresenceStore(t, func(*testing.T) realtime.PresenceStore { return realtimetest.NewPresence() })
}
