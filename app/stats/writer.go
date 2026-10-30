package stats

import "time"

const (
	statQueueSize     = 4096
	statBatchSize     = 256
	statFlushInterval = time.Second
)

type statRow struct {
	chatID   int64
	senderID int64 // 0 = unknown
	isGroup  bool
	at       time.Time
	kind     string
}

// enqueueStat hands a row to statWriter without blocking the update loop; when the queue is
// full the row is dropped (stats are best effort).
func (d *DistortionerDB) enqueueStat(row statRow) {
	d.statsMu.RLock()
	defer d.statsMu.RUnlock()
	if d.statsDone {
		return
	}
	select {
	case d.statRows <- row:
	default:
		d.logger.Warn("stats queue full, dropping row")
	}
}

// FlushStats writes every row queued so far. Used by tests; the writer flushes on its own.
func (d *DistortionerDB) FlushStats() {
	ack := make(chan struct{})
	select {
	case d.flushReq <- ack:
		<-ack
	case <-d.writerEnd:
	}
}

func (d *DistortionerDB) stopStatWriter() {
	d.statsMu.Lock()
	if d.statsDone {
		d.statsMu.Unlock()
		return
	}
	d.statsDone = true
	close(d.statRows)
	d.statsMu.Unlock()
	<-d.writerEnd
}

// statWriter inserts queued rows in one transaction per batch instead of one write per message.
func (d *DistortionerDB) statWriter() {
	defer close(d.writerEnd)
	ticker := time.NewTicker(statFlushInterval)
	defer ticker.Stop()
	batch := make([]statRow, 0, statBatchSize)
	flush := func() {
		if len(batch) == 0 {
			return
		}
		if err := d.insertStats(batch); err != nil {
			d.logger.Errorw("write stats", "err", err, "rows", len(batch))
		}
		batch = batch[:0]
	}
	for {
		select {
		case row, ok := <-d.statRows:
			if !ok {
				flush()
				return
			}
			batch = append(batch, row)
			if len(batch) >= statBatchSize {
				flush()
			}
		case <-ticker.C:
			flush()
		case ack := <-d.flushReq:
			// Rows queued before the flush request are already buffered in the channel.
			for drained := false; !drained; {
				select {
				case row := <-d.statRows:
					batch = append(batch, row)
				default:
					drained = true
				}
			}
			flush()
			close(ack)
		}
	}
}

func (d *DistortionerDB) insertStats(rows []statRow) error {
	tx, err := d.db.Begin()
	if err != nil {
		return err
	}
	stmt := tx.Stmt(d.insert)
	for _, r := range rows {
		var sender any
		if r.senderID != 0 {
			sender = r.senderID
		}
		if _, err := stmt.Exec(r.chatID, sender, r.isGroup, r.at, r.kind); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}
