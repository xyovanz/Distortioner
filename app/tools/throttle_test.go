package tools

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestThrottle(t *testing.T) {
	th := NewThrottle(time.Hour)
	now := time.Now()
	require.True(t, th.Allow(1, now))
	require.False(t, th.Allow(1, now.Add(30*time.Minute)))
	require.True(t, th.Allow(2, now))
	require.True(t, th.Allow(1, now.Add(time.Hour)))
	th.Reset(1)
	require.True(t, th.Allow(1, now.Add(time.Hour+time.Minute)))
}
