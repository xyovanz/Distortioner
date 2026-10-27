//go:build cgo

package stats

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	tb "gopkg.in/telebot.v3"
)

func testDB(t *testing.T) *DistortionerDB {
	t.Helper()
	d := openDB(zap.NewExample().Sugar(), filepath.Join(t.TempDir(), "test.sqlite"))
	t.Cleanup(d.Close)
	return d
}

func TestTouchUserAndLookup(t *testing.T) {
	d := testDB(t)
	d.TouchUser(&tb.User{ID: 42, Username: "Cat", FirstName: "Tom"})
	d.TouchUser(&tb.User{ID: 42, Username: "Cat2", FirstName: "Tom", LastName: "C"})
	d.TouchUser(&tb.User{ID: 7, IsBot: true, Username: "somebot"})

	u, ok := d.GetUser(42)
	require.True(t, ok)
	require.Equal(t, "Cat2", u.Username)
	require.Equal(t, "Tom C (@Cat2)", u.DisplayName())
	require.False(t, u.FirstSeen.IsZero())

	found, ok := d.FindUserByUsername("@cat2")
	require.True(t, ok)
	require.EqualValues(t, 42, found.ID)
	_, ok = d.FindUserByUsername("somebot")
	require.False(t, ok)

	total, err := d.TotalUsers()
	require.NoError(t, err)
	require.Equal(t, 1, total)
	newUsers, err := d.NewUsers(Daily)
	require.NoError(t, err)
	require.Equal(t, 1, newUsers)
}

func TestSaveStatTracksSender(t *testing.T) {
	d := testDB(t)
	alice := &tb.User{ID: 1}
	bob := &tb.User{ID: 2}
	group := &tb.Chat{ID: -100, Type: tb.ChatSuperGroup}
	photo := &tb.Message{Chat: group, Sender: alice, Photo: &tb.Photo{}}
	d.SaveStat(photo, false)
	d.SaveStat(&tb.Message{Chat: group, Sender: alice, Text: "hi"}, false)
	// Bob replies with /distort to Alice's photo: counts for Bob.
	d.SaveStat(&tb.Message{Chat: group, Sender: bob, Text: "/distort", ReplyTo: photo}, true)

	s, err := d.GetUserStat(1, Daily)
	require.NoError(t, err)
	require.Equal(t, UserStat{Total: 2, Photo: 1, Text: 1}, s)
	s, err = d.GetUserStat(2, "")
	require.NoError(t, err)
	require.Equal(t, UserStat{Total: 1, Photo: 1}, s)

	top, err := d.TopUsers(Weekly, 10)
	require.NoError(t, err)
	require.Len(t, top, 2)
	require.EqualValues(t, 1, top[0].User.ID)
	require.Equal(t, 2, top[0].Count)

	stat, err := d.GetStat(Daily)
	require.NoError(t, err)
	require.Equal(t, 3, stat.Interactions)
	require.Equal(t, 1, stat.Chats)
	require.Equal(t, 2, stat.Users)
}

func TestBanLifecycle(t *testing.T) {
	d := testDB(t)
	const admin = 999

	ok, err := d.BanUser(5, admin, "spam")
	require.NoError(t, err)
	require.True(t, ok)
	reason, banned := d.BanReason(5)
	require.True(t, banned)
	require.Equal(t, "spam", reason)

	// Same reason or empty reason: no change.
	ok, err = d.BanUser(5, admin, "")
	require.NoError(t, err)
	require.False(t, ok)
	// New reason replaces the old one.
	ok, err = d.BanUser(5, admin, "nsfw")
	require.NoError(t, err)
	require.False(t, ok)
	ban, found := d.GetBan(5)
	require.True(t, found)
	require.Equal(t, "nsfw", ban.Reason)
	require.EqualValues(t, admin, ban.BannedBy)

	require.NoError(t, d.LogUnbanRequest(5, "sorry"))
	ok, err = d.UnbanUser(5, admin, "forgiven")
	require.NoError(t, err)
	require.True(t, ok)
	require.False(t, d.IsBanned(5))
	ok, err = d.UnbanUser(5, admin, "")
	require.NoError(t, err)
	require.False(t, ok)

	hist, err := d.BanHistory(5, 10)
	require.NoError(t, err)
	actions := make([]string, len(hist))
	for i, e := range hist {
		actions[i] = e.Action
	}
	require.Equal(t, []string{ActionUnban, ActionUnbanRequest, ActionBanReasonEdit, ActionBan}, actions)
	require.Equal(t, "forgiven", hist[0].Reason)
}

func TestBansSurviveReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.sqlite")
	d := openDB(zap.NewExample().Sugar(), path)
	_, err := d.BanUser(8, 1, "abuse")
	require.NoError(t, err)
	d.TouchUser(&tb.User{ID: 8, Username: "eight"})
	d.Close()

	d = openDB(zap.NewExample().Sugar(), path)
	defer d.Close()
	reason, banned := d.BanReason(8)
	require.True(t, banned)
	require.Equal(t, "abuse", reason)
	bans, err := d.BannedUsers()
	require.NoError(t, err)
	require.Len(t, bans, 1)
	require.Equal(t, "eight", bans[0].User.Username)
}
