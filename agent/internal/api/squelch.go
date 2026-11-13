package api

import (
	"sync"
	"time"

	"parkways-sentinel/agent/internal/model"
)

// squelchWindow is how long one device+event counter lives.
const squelchWindow = time.Hour

// squelcher caps how many events with the same name one device can store
// per window. A kiosk reporting a dead reader on every poll cycle still
// gets its first `limit` events through (enough to see the problem); the
// rest of the hour is collapsed into one summary event with the held
// count, so the database, the dashboard and Telegram stay readable and
// the agent's work per event stays constant.
type squelcher struct {
	mu     sync.Mutex
	limit  int
	window time.Duration
	m      map[string]*squelchEntry
}

type squelchEntry struct {
	start time.Time
	count int               // events seen this window
	held  int               // events not stored
	last  model.StoredEvent // the newest stored event, template for the summary
}

// squelchSummary is what a finished window with held events turns into.
type squelchSummary struct {
	template model.StoredEvent
	held     int
	from, to time.Time
}

func newSquelcher(limit int) *squelcher {
	return &squelcher{limit: limit, window: squelchWindow, m: map[string]*squelchEntry{}}
}

// admit decides whether an event is stored. It returns held=true when the
// event is dropped in favour of the window summary, and a summary when
// this event closed the previous window.
func (q *squelcher) admit(key string, now time.Time) (held bool, sum *squelchSummary) {
	if q == nil || q.limit <= 0 {
		return false, nil
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	e := q.m[key]
	if e != nil && now.Sub(e.start) >= q.window {
		sum = e.summary(now)
		delete(q.m, key)
		e = nil
	}
	if e == nil {
		q.m[key] = &squelchEntry{start: now, count: 1}
		return false, sum
	}
	e.count++
	if e.count > q.limit {
		e.held++
		return true, sum
	}
	return false, sum
}

// stored records the newest stored event for the key, used as the
// summary's template (device, codes, severity).
func (q *squelcher) stored(key string, ev model.StoredEvent) {
	if q == nil || q.limit <= 0 {
		return
	}
	q.mu.Lock()
	if e := q.m[key]; e != nil {
		e.last = ev
	}
	q.mu.Unlock()
}

// drain closes windows that ended without a new event arriving and
// returns their summaries. Idle counters are dropped, so memory is
// bounded by the number of distinct device+event pairs seen in an hour.
func (q *squelcher) drain(now time.Time) []squelchSummary {
	if q == nil || q.limit <= 0 {
		return nil
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	var out []squelchSummary
	for k, e := range q.m {
		if now.Sub(e.start) < q.window {
			continue
		}
		if s := e.summary(now); s != nil {
			out = append(out, *s)
		}
		delete(q.m, k)
	}
	return out
}

func (e *squelchEntry) summary(now time.Time) *squelchSummary {
	if e.held == 0 || e.last.EventID == "" {
		return nil
	}
	return &squelchSummary{template: e.last, held: e.held, from: e.start, to: now}
}
