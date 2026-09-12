package queue

import "time"

type Job struct {
	runnable      func()    // The job itself
	userID        int64     // Chat ID used for fairness / per-chat job counts
	senderID      int64     // Telegram user who triggered the job (admin ban matching)
	priority      int       // The priority of the item in the queue. Lesser numbers mean bigger priority. Calculated by the HonestJobQueue
	insertionTime time.Time // Needed to maintain insertion-order for items with equal priority.
	seq           int64     // Tie-break when insertionTime collides (Windows clock resolution).
}

func newJob(userID, senderID int64, priority int, seq int64, runnable func()) *Job {
	return &Job{
		runnable:      runnable,
		userID:        userID,
		senderID:      senderID,
		priority:      priority,
		insertionTime: time.Now(),
		seq:           seq,
	}
}

func (j *Job) Run() {
	j.runnable()
}
