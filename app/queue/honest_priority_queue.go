package queue

import (
	"container/heap"
	"sync"
	"time"

	"github.com/pkg/errors"
)

// MaxQueueLen is the maximum number of jobs that may sit in the queue.
// Push rejects when Len() > MaxQueueLen, so the queue can hold MaxQueueLen+1 jobs.
// VideoWorker.messenger must be buffered to at least MaxQueueLen+1 or Submit blocks
// the Telegram update loop once the channel fills while workers are busy.
const MaxQueueLen = 2000

// HonestJobQueue It ain't much, but it's an honest job.jpg
// Wraps PriorityQueue to make it thread-safe. Manages priorities.
// Extremely inefficient, but works for my use-case (very slow jobs and small queue sizes)
type HonestJobQueue struct {
	mu            *sync.RWMutex
	queue         PriorityQueue
	users         map[int64]int // Tracks the amount of jobs per chat currently in the queue. Used to calculate priority
	softBanned    map[int64]any // Drop jobs for these chat IDs (bot was blocked); cleared on Push / when drained
	adminBanned   map[int64]any // Sticky sender bans from admin /ban; only UnbanUser clears
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
		softBanned:    make(map[int64]any),
		adminBanned:   make(map[int64]any),
		priorityChats: priorityChatsMap,
	}
}

// BanUser soft-bans a chat (bot was blocked). Remaining jobs for that chat are
// dropped on Pop. Lifted after those jobs are skipped, or when the chat Push'es again.
func (hjq *HonestJobQueue) BanUser(chatID int64) {
	hjq.mu.Lock()
	defer hjq.mu.Unlock()

	hjq.softBanned[chatID] = nil
}

// BanSender sticky-bans a Telegram user so their queued jobs are dropped until UnbanUser.
// Matches Job.senderID (not chat ID), so group-queued work is cancelled correctly.
func (hjq *HonestJobQueue) BanSender(senderID int64) {
	hjq.mu.Lock()
	defer hjq.mu.Unlock()

	hjq.adminBanned[senderID] = nil
}

// UnbanUser clears a sticky admin ban.
func (hjq *HonestJobQueue) UnbanUser(senderID int64) {
	hjq.mu.Lock()
	defer hjq.mu.Unlock()

	delete(hjq.adminBanned, senderID)
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

// Position returns 1-based queue position for the chat's earliest job, or 0 if none.
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

func (hjq *HonestJobQueue) dropJobCount(chatID int64) {
	hjq.users[chatID]--
	if hjq.users[chatID] <= 0 {
		delete(hjq.users, chatID)
	}
}

func (hjq *HonestJobQueue) dropSoftBannedJob(chatID int64) {
	hjq.dropJobCount(chatID)
	if _, stillHasJobs := hjq.users[chatID]; !stillHasJobs {
		// All of this chat's queued jobs are gone; lift the soft ban.
		delete(hjq.softBanned, chatID)
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

		_, soft := hjq.softBanned[job.userID]
		_, admin := hjq.adminBanned[job.senderID]
		if soft || admin {
			// Skip this job only; do not drain unrelated chats' work.
			if soft {
				hjq.dropSoftBannedJob(job.userID)
			} else {
				hjq.dropJobCount(job.userID)
			}
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

// Push enqueues work for chatID (fairness key). senderID is who triggered it (admin bans).
func (hjq *HonestJobQueue) Push(chatID, senderID int64, runnable func()) (int, error) {
	hjq.mu.Lock()
	defer hjq.mu.Unlock()

	if hjq.maintenance {
		return 0, errors.New("The server is on temporary maintenance, no new videos are being processed at the moment, try again later")
	}

	if hjq.queue.Len() > MaxQueueLen {
		return 0, errors.New("There are too many items queued already, try again later")
	}

	if _, banned := hjq.adminBanned[senderID]; banned {
		return 0, errors.New("You are banned from using this bot")
	}

	// users[id] is the queued job count. Job heap priority is derived separately so
	// priority chats can keep a boosted sort key without corrupting that count.
	// (Previously priority chats wrote users[id]=-1, so BanUser/dropBannedJob treated
	// the first skipped job as "all done" and lifted the ban — remaining jobs ran.)
	count := hjq.users[chatID]
	_, isPriority := hjq.priorityChats[chatID]
	if !isPriority && count > 2 {
		// Do not touch users[chatID]: we never incremented for this rejected push.
		// Decrementing here lets a user grow past the limit (reject → count drops → next push succeeds).
		return 0, errors.New("You're distorting videos too often, wait until the previous ones have been processed")
	}

	// Soft ban only: if this chat messaged us again, the bot is no longer blocked.
	// Admin bans must stay until UnbanUser — clearing them here let in-flight
	// Submit races undo /ban while queued jobs were still waiting.
	delete(hjq.softBanned, chatID)

	hjq.users[chatID] = count + 1

	jobPriority := count
	if isPriority {
		jobPriority = -2
	}

	hjq.seq++
	heap.Push(&hjq.queue, newJob(chatID, senderID, jobPriority, hjq.seq, runnable))

	return hjq.positionLocked(chatID), nil
}
