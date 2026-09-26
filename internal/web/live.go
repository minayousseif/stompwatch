package web

import (
	"sync"

	"github.com/minayousseif/stompwatch/internal/meter"
)

// liveQueue is how many seconds a browser may fall behind before it starts
// losing updates. A live meter wants the newest number, so a deep queue would
// only show an older one later.
const liveQueue = 4

// LiveFeed carries the newest measured second to every open browser. The
// collector must never wait for a browser, so a subscriber that is not
// keeping up loses updates rather than slowing the measurement down.
type LiveFeed struct {
	mu   sync.Mutex
	subs map[int]chan meter.Bin
	next int
}

// NewLiveFeed returns a feed with no subscribers.
func NewLiveFeed() *LiveFeed {
	return &LiveFeed{subs: make(map[int]chan meter.Bin)}
}

// Publish is called from the DSP goroutine. It never blocks.
func (f *LiveFeed) Publish(b meter.Bin) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, ch := range f.subs {
		select {
		case ch <- b:
		default:
			// This browser is behind. Drop the second rather than wait.
		}
	}
}

// Subscribe returns a channel of bins and a function that stops the
// subscription and closes the channel. Calling the function twice is safe.
func (f *LiveFeed) Subscribe() (<-chan meter.Bin, func()) {
	ch := make(chan meter.Bin, liveQueue)
	f.mu.Lock()
	id := f.next
	f.next++
	f.subs[id] = ch
	f.mu.Unlock()

	var once sync.Once
	return ch, func() {
		once.Do(func() {
			f.mu.Lock()
			delete(f.subs, id)
			f.mu.Unlock()
			// Closed after it is out of the map, so Publish never sends on a
			// closed channel.
			close(ch)
		})
	}
}
