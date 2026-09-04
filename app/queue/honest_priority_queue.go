package queue

import (
	"container/heap"
	"github.com/pkg/errors"
	"sync"
	"time"
)

// HonestJobQueue It ain't much, but it's an honest job.jpg
// Wraps PriorityQueue to make it thread-safe. Manages priorities.
// Extremely inefficient, but works for my use-case (very slow jobs and small queue sizes)
type HonestJobQueue struct {
	mu            *sync.RWMutex
	queue         PriorityQueue
	users         map[int64]int // Tracks the amount of job per-user currently in the queue. Used to calculate priority
	banned        map[int64]any // Drop jobs from these users
	maintenance   bool
	priorityChats map[int64]any // not very honest of an honest job queue, but I don't care, I'm not waiting with everybody else
	seq           int64         // stable tie-break when insertion times collide
}

func NewHonestJobQueue(initialCapacity int, priorityChats []int64) *HonestJobQueue {
	priorityChatsMap := make(map[int64]any)
	for _, chat := range priorityChats {
		priorityChatsMap[chat] = nil
	}
	return &HonestJobQueue{
		mu:            &sync.RWMutex{},
		queue:         make(PriorityQueue, 0, initialCapacity),
		users:         make(map[int64]int),
		banned:        make(map[int64]any),
		priorityChats: priorityChatsMap,
	}
}

// BanUser This will "ban" the user (if they were impatient and banned the bot first)
// causing their jobs to be dropped when they pop up. Once all the jobs have been popped
// the ban will be lifted
func (hjq *HonestJobQueue) BanUser(userID int64) {
	hjq.mu.Lock()
	defer hjq.mu.Unlock()

	hjq.banned[userID] = nil
}

func (hjq *HonestJobQueue) updatePriorities(userID int64) {
	now := time.Now()
	for _, job := range hjq.queue {
		if job.userID != userID {
			continue
		}
		job.priority--
		// Re-enter fairly: bump seq so equal-timestamp ties (common on Windows) lose to older jobs.
		hjq.seq++
		job.seq = hjq.seq
		job.insertionTime = now
	}
	heap.Init(&hjq.queue)
}

func (hjq *HonestJobQueue) Len() int {
	hjq.mu.RLock()
	defer hjq.mu.RUnlock()

	return len(hjq.queue)
}

func (hjq *HonestJobQueue) Stats() (int, int) {
	hjq.mu.RLock()
	defer hjq.mu.RUnlock()

	return len(hjq.queue), len(hjq.users)
}

// Position returns 1-based queue position for the user's earliest job, or 0 if none.
func (hjq *HonestJobQueue) Position(userID int64) int {
	hjq.mu.RLock()
	defer hjq.mu.RUnlock()
	return hjq.positionLocked(userID)
}

func (hjq *HonestJobQueue) positionLocked(userID int64) int {
	var target *Job
	for _, job := range hjq.queue {
		if job.userID != userID {
			continue
		}
		if target == nil || jobBefore(job, target) {
			target = job
		}
	}
	if target == nil {
		return 0
	}
	pos := 1
	for _, job := range hjq.queue {
		if job == target {
			continue
		}
		if jobBefore(job, target) {
			pos++
		}
	}
	return pos
}

func jobBefore(a, b *Job) bool {
	if a.priority != b.priority {
		return a.priority < b.priority
	}
	if !a.insertionTime.Equal(b.insertionTime) {
		return a.insertionTime.Before(b.insertionTime)
	}
	return a.seq < b.seq
}

func (hjq *HonestJobQueue) Maintenance() bool {
	hjq.mu.RLock()
	defer hjq.mu.RUnlock()
	return hjq.maintenance
}

func (hjq *HonestJobQueue) dropBannedJob(userID int64) {
	hjq.users[userID]--
	if hjq.users[userID] <= 0 {
		delete(hjq.users, userID)
		// All of this user's queued jobs are gone; lift the ban.
		delete(hjq.banned, userID)
	}
}

func (hjq *HonestJobQueue) Pop() *Job {
	hjq.mu.Lock()
	defer hjq.mu.Unlock()

	for {
		if hjq.queue.Len() == 0 {
			return nil
		}
		job := heap.Pop(&hjq.queue).(*Job)

		if _, banned := hjq.banned[job.userID]; banned {
			// Skip this job only; do not drain unrelated users' work.
			hjq.dropBannedJob(job.userID)
			continue
		}

		hjq.users[job.userID]--
		if hjq.users[job.userID] == 0 {
			delete(hjq.users, job.userID)
		}

		hjq.updatePriorities(job.userID)
		return job
	}
}

func (hjq *HonestJobQueue) ToggleMaintenance() bool {
	hjq.mu.Lock()
	defer hjq.mu.Unlock()

	hjq.maintenance = !hjq.maintenance

	return hjq.maintenance
}

func (hjq *HonestJobQueue) Push(userID int64, runnable func()) (int, error) {
	hjq.mu.Lock()
	defer hjq.mu.Unlock()

	if hjq.maintenance {
		return 0, errors.New("The server is on temporary maintenance, no new videos are being processed at the moment, try again later")
	}

	if hjq.queue.Len() > 2000 {
		return 0, errors.New("There are too many items queued already, try again later")
	}
	priority := hjq.users[userID]
	_, ok := hjq.priorityChats[userID]
	if ok {
		priority = -2
	}

	if priority > 2 {
		hjq.users[userID]--
		return 0, errors.New("You're distorting videos too often, wait until the previous ones have been processed")
	}

	// if a user sent us a message then we're clearly unbanned
	if _, ok := hjq.banned[userID]; ok {
		delete(hjq.banned, userID)
	}

	hjq.users[userID] = priority + 1

	hjq.seq++
	heap.Push(&hjq.queue, newJob(userID, priority, hjq.seq, runnable))

	return hjq.positionLocked(userID), nil
}
