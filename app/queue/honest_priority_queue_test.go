package queue

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestHonestJobQueue_InsertionOrder(t *testing.T) {
	hjq := NewHonestJobQueue(50, []int64{})
	for id := int64(1); id < 4; id++ {
		hjq.Push(id, func() {})
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
		hjq.Push(1, func() {})
	}
	// one job from user 3
	hjq.Push(3, func() {})
	// two jobs from user 2
	for i := 0; i < 2; i++ {
		hjq.Push(2, func() {})
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

	assert.NoError(t, hjq.Push(1, func() {}))
	assert.NoError(t, hjq.Push(1, func() {}))
	assert.NoError(t, hjq.Push(2, func() {}))
	assert.NoError(t, hjq.Push(3, func() {}))

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
	assert.NoError(t, hjq.Push(1, func() {}))
	revived := hjq.Pop()
	if assert.NotNil(t, revived) {
		assert.Equal(t, int64(1), revived.userID)
	}
}
