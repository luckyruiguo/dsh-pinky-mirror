package gateway

import (
	"sync"
	"testing"
	"time"
)

// testClock moves only when the test says, a Windows clock tick at a
// time: until then every reading is the same, as time.Now's are on Windows
// within a tick.
type testClock struct {
	mu  sync.Mutex
	now time.Time
}

// windowsClock makes stamp read a testClock until the test ends.
func windowsClock(t *testing.T) *testClock {
	c := &testClock{now: time.Now()}
	clock.mu.Lock()
	prev := clock.now
	clock.now = c.read
	clock.mu.Unlock()
	t.Cleanup(func() {
		clock.mu.Lock()
		clock.now = prev
		clock.mu.Unlock()
	})
	return c
}

func (c *testClock) read() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// tick moves the clock on by Windows' default tick.
func (c *testClock) tick() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(15625 * time.Microsecond)
}

func TestStampsNeverRepeat(t *testing.T) {
	c := windowsClock(t)
	base := stamp()
	var mu sync.Mutex
	seen := map[time.Duration]bool{}
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			last := base
			for i := range 500 {
				if i%100 == 0 {
					c.tick()
				}
				s := stamp()
				if !s.After(last) {
					t.Errorf("stamp %v is not after the one before, %v", s.Sub(base), last.Sub(base))
					return
				}
				last = s
				mu.Lock()
				if seen[s.Sub(base)] {
					t.Errorf("stamp %v given twice", s.Sub(base))
				}
				seen[s.Sub(base)] = true
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
}
