package tools

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestInitTempDirRemovesOnlyStaleJobFiles(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DISTORTIONER_TMP_DIR", dir)
	t.Setenv("DISTORTIONER_KEEP_FAILED", "")
	stale := "0b9e3c1a-5f4d-4e2b-9a8c-7d6e5f4a3b2c"
	require.NoError(t, os.WriteFile(filepath.Join(dir, stale+".mp4"), nil, 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, stale+"Frames"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "keep-me.txt"), nil, 0o644))

	got, err := InitTempDir()
	require.NoError(t, err)
	require.Equal(t, dir, got)
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, "keep-me.txt", entries[0].Name())
}

func TestRateLimiterForgetsExpiredChats(t *testing.T) {
	r := NewRateLimiter()
	r.GetRateOverPeriod(1, 1000)
	r.GetRateOverPeriod(2, 1000)
	count, _ := r.GetRateOverPeriod(1, 1000+TimePeriodSeconds)
	require.Equal(t, 1, count, "expired range restarts")
	require.Len(t, r.usersRate, 1, "chat 2 swept")
}
