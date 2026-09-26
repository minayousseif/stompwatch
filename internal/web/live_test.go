package web

import (
	"sync"
	"testing"
	"time"

	"github.com/minayousseif/stompwatch/internal/meter"
)

func TestLiveFeedDeliversToEverySubscriber(t *testing.T) {
	f := NewLiveFeed()
	a, stopA := f.Subscribe()
	b, stopB := f.Subscribe()
	defer stopA()
	defer stopB()

	f.Publish(bin(0, 41.2, 55, 33))

	for name, ch := range map[string]<-chan meter.Bin{"a": a, "b": b} {
		select {
		case got := <-ch:
			if got.LAeq != 41.2 || got.LAmax != 55 || got.Baseline != 33 {
				t.Errorf("subscriber %s got %+v", name, got)
			}
		case <-time.After(time.Second):
			t.Fatalf("subscriber %s received nothing", name)
		}
	}
}

// The DSP goroutine must never wait for a browser. Publish has to return even
// when nobody is listening and when a subscriber has stopped reading.
func TestPublishNeverBlocks(t *testing.T) {
	f := NewLiveFeed()

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := range 10 {
			f.Publish(bin(i, 40, 50, 30)) // no subscribers at all
		}
		ch, stop := f.Subscribe()
		defer stop()
		// Fill whatever buffer the subscriber has, and keep going. A reader
		// that never reads must not stop the measurement.
		for i := range 10000 {
			f.Publish(bin(i, 40, 50, 30))
		}
		_ = ch
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Publish blocked; the DSP goroutine would have stalled")
	}
}

func TestUnsubscribeStopsDelivery(t *testing.T) {
	f := NewLiveFeed()
	ch, stop := f.Subscribe()
	stop()

	f.Publish(bin(0, 41, 55, 33))

	select {
	case _, open := <-ch:
		if open {
			t.Fatal("a stopped subscriber still received a bin")
		}
	case <-time.After(time.Second):
		t.Fatal("the channel of a stopped subscriber is neither closed nor empty")
	}
	stop() // a second stop must be harmless
}

// Two goroutines publishing while subscribers come and go, under -race.
func TestLiveFeedIsSafeForConcurrentUse(t *testing.T) {
	f := NewLiveFeed()
	var wg sync.WaitGroup
	stop := make(chan struct{})

	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					f.Publish(bin(0, 40, 50, 30))
				}
			}
		}()
	}
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 50 {
				ch, off := f.Subscribe()
				select {
				case <-ch:
				default:
				}
				off()
			}
		}()
	}
	time.Sleep(20 * time.Millisecond)
	close(stop)
	wg.Wait()
}
