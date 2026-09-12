package queue

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestHonestJobQueue_InsertionOrder(t *testing.T) {
	hjq := NewHonestJobQueue(50, []int64{})
	for id := int64(1); id < 4; id++ {
		_, err := hjq.Push(id, id, func() {})
		assert.NoError(t, err)
	}
	assert.Equal(t, 3, hjq.Len())
	for id := int64(1); id < 4; id++ {
		job := hjq.Pop()
		assert.Equal(t, id, job.userID)
	}
	assert.Equal(t, 0, hjq.Len())
}

func TestHonestJobQueue_RepeatUsers(t *testing.T) {
	hjq := NewHonestJobQueue(50, []int64{})

	// three jobs by user 1
	for i := 0; i < 3; i++ {
		_, err := hjq.Push(1, 1, func() {})
		assert.NoError(t, err)
	}
	// one job from user 3
	_, err := hjq.Push(3, 3, func() {})
	assert.NoError(t, err)
	// two jobs from user 2
	for i := 0; i < 2; i++ {
		_, err := hjq.Push(2, 2, func() {})
		assert.NoError(t, err)
	}

	assert.Equal(t, 6, hjq.Len())

	poppedIDs := make([]int64, 0, 6)
	for i := 0; i < 6; i++ {
		poppedIDs = append(poppedIDs, hjq.Pop().userID)
	}

	assert.Equal(t, []int64{1, 3, 2, 1, 2, 1}, poppedIDs)
}

// BanUser must drop only that user's jobs. A previous loop left the ban flag
// sticky and drained every remaining job once any banned job reached the front.
func TestHonestJobQueue_BanSkipsOnlyBannedUser(t *testing.T) {
	hjq := NewHonestJobQueue(50, []int64{})

	_, err := hjq.Push(1, 1, func() {})
	assert.NoError(t, err)
	_, err = hjq.Push(1, 1, func() {})
	assert.NoError(t, err)
	_, err = hjq.Push(2, 2, func() {})
	assert.NoError(t, err)
	_, err = hjq.Push(3, 3, func() {})
	assert.NoError(t, err)

	hjq.BanUser(1)

	first := hjq.Pop()
	if assert.NotNil(t, first) {
		assert.Equal(t, int64(2), first.userID)
	}
	second := hjq.Pop()
	if assert.NotNil(t, second) {
		assert.Equal(t, int64(3), second.userID)
	}
	assert.Nil(t, hjq.Pop())
	assert.Equal(t, 0, hjq.Len())

	// Ban lifts after banned user's jobs are exhausted; they can enqueue again.
	_, err = hjq.Push(1, 1, func() {})
	assert.NoError(t, err)
	revived := hjq.Pop()
	if assert.NotNil(t, revived) {
		assert.Equal(t, int64(1), revived.userID)
	}
}

func TestHonestJobQueue_Position(t *testing.T) {
	hjq := NewHonestJobQueue(50, []int64{})
	pos1, err := hjq.Push(1, 1, func() {})
	assert.NoError(t, err)
	assert.Equal(t, 1, pos1)
	pos2, err := hjq.Push(2, 2, func() {})
	assert.NoError(t, err)
	assert.Equal(t, 2, pos2)
	pos3, err := hjq.Push(3, 3, func() {})
	assert.NoError(t, err)
	assert.Equal(t, 3, pos3)
	assert.Equal(t, 1, hjq.Position(1))
	assert.Equal(t, 2, hjq.Position(2))
	assert.Equal(t, 3, hjq.Position(3))
}

// Priority chats used to store users[id]=-1 (boosted priority written back into the
// count). dropBannedJob then saw <=0 after one skip and lifted the ban, so the rest
// of that user's queued jobs still ran.
func TestHonestJobQueue_BanDropsAllPriorityChatJobs(t *testing.T) {
	hjq := NewHonestJobQueue(50, []int64{42})
	for i := 0; i < 3; i++ {
		_, err := hjq.Push(42, 42, func() {})
		assert.NoError(t, err)
	}
	hjq.BanUser(42)

	assert.Nil(t, hjq.Pop())
	assert.Equal(t, 0, hjq.Len())

	_, users := hjq.Stats()
	assert.Equal(t, 0, users)
}

func TestHonestJobQueue_PriorityChatTracksUserCount(t *testing.T) {
	hjq := NewHonestJobQueue(50, []int64{42})
	for i := 0; i < 5; i++ {
		_, err := hjq.Push(42, 42, func() {})
		assert.NoError(t, err)
	}
	assert.Equal(t, 5, hjq.Len())
	_, users := hjq.Stats()
	assert.Equal(t, 1, users)

	for i := 0; i < 5; i++ {
		assert.NotNil(t, hjq.Pop())
	}
	assert.Equal(t, 0, hjq.Len())
	_, users = hjq.Stats()
	assert.Equal(t, 0, users)
}

// Rejected pushes must not mutate the per-user job count. A leftover decrement
// from an older increment-then-check pattern let users grow past the 3-job cap.
func TestHonestJobQueue_RejectDoesNotBypassLimit(t *testing.T) {
	hjq := NewHonestJobQueue(50, []int64{})
	for i := 0; i < 3; i++ {
		_, err := hjq.Push(1, 1, func() {})
		assert.NoError(t, err)
	}
	assert.Equal(t, 3, hjq.Len())

	for i := 0; i < 5; i++ {
		_, err := hjq.Push(1, 1, func() {})
		assert.Error(t, err)
	}
	assert.Equal(t, 3, hjq.Len())

	// After one job completes, exactly one new push should be allowed.
	assert.Equal(t, int64(1), hjq.Pop().userID)
	_, err := hjq.Push(1, 1, func() {})
	assert.NoError(t, err)
	_, err = hjq.Push(1, 1, func() {})
	assert.Error(t, err)
	assert.Equal(t, 3, hjq.Len())
}


// Admin BanSender must match Job.senderID even when fairness key is a group chat ID.
func TestHonestJobQueue_AdminBanDropsGroupJobsBySender(t *testing.T) {
	hjq := NewHonestJobQueue(50, []int64{})
	groupID := int64(-100)
	abuser := int64(7)
	other := int64(8)

	_, err := hjq.Push(groupID, abuser, func() {})
	assert.NoError(t, err)
	_, err = hjq.Push(groupID, abuser, func() {})
	assert.NoError(t, err)
	_, err = hjq.Push(groupID, other, func() {})
	assert.NoError(t, err)

	hjq.BanSender(abuser)

	job := hjq.Pop()
	if assert.NotNil(t, job) {
		assert.Equal(t, groupID, job.userID)
		assert.Equal(t, other, job.senderID)
	}
	assert.Nil(t, hjq.Pop())
	assert.Equal(t, 0, hjq.Len())
}

// Push must not clear sticky admin bans (that undid /ban under in-flight Submit races).
func TestHonestJobQueue_PushDoesNotClearAdminBan(t *testing.T) {
	hjq := NewHonestJobQueue(50, []int64{})
	_, err := hjq.Push(1, 1, func() {})
	assert.NoError(t, err)
	hjq.BanSender(1)

	_, err = hjq.Push(1, 1, func() {})
	assert.Error(t, err)

	assert.Nil(t, hjq.Pop())
	assert.Equal(t, 0, hjq.Len())

	hjq.UnbanUser(1)
	_, err = hjq.Push(1, 1, func() {})
	assert.NoError(t, err)
	revived := hjq.Pop()
	if assert.NotNil(t, revived) {
		assert.Equal(t, int64(1), revived.userID)
	}
}

// Soft BanUser still clears when the chat Push'es again (bot was unblocked).
func TestHonestJobQueue_PushClearsSoftBan(t *testing.T) {
	hjq := NewHonestJobQueue(50, []int64{})
	_, err := hjq.Push(1, 1, func() {})
	assert.NoError(t, err)
	hjq.BanUser(1) // soft

	_, err = hjq.Push(1, 1, func() {})
	assert.NoError(t, err)

	// First queued job was soft-banned at ban time; second Push cleared soft ban so
	// both remaining? Actually first job still in queue was soft-banned then soft ban cleared.
	// After Push cleared soft ban, both jobs run.
	assert.NotNil(t, hjq.Pop())
	assert.NotNil(t, hjq.Pop())
}
