package audit

import (
	"container/list"
	"sync"
	"time"
)

// Collapse windows and the open-key cap (design §9.2, §9.4).
const (
	// SummaryWindow is how often summary records are flushed.
	SummaryWindow = 5 * time.Minute
	// DenialWindow is how long repeated denials of one request collapse into one record.
	DenialWindow = time.Minute
	// MaxOpenKeys is the most open keys a window holds; past it the oldest is flushed early.
	MaxOpenKeys = 10000
)

// window collapses records by key until it is taken, each with its count.
type window struct {
	mu    sync.Mutex
	max   int
	open  map[string]*list.Element
	order *list.List // of *Record, oldest first
}

func newWindow(max int) *window {
	return &window{max: max, open: map[string]*list.Element{}, order: list.New()}
}

// add counts rec under key. A new key past the cap evicts the oldest open
// record, which add returns for writing now.
func (w *window) add(key string, rec Record) (evicted *Record) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if e, ok := w.open[key]; ok {
		e.Value.(*Record).Count++
		return nil
	}
	if w.order.Len() >= w.max {
		front := w.order.Front()
		evicted = w.order.Remove(front).(*Record)
		delete(w.open, evicted.key)
	}
	rec.Count, rec.key = 1, key
	w.open[key] = w.order.PushBack(&rec)
	return evicted
}

// take returns the open records, oldest first, and empties the window.
func (w *window) take() []Record {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([]Record, 0, w.order.Len())
	for e := w.order.Front(); e != nil; e = e.Next() {
		out = append(out, *e.Value.(*Record))
	}
	w.open, w.order = map[string]*list.Element{}, list.New()
	return out
}
