package metrics

import (
	"sync"
	"testing"
)

func TestCounterIncAndValue(t *testing.T) {
	var c Counter
	if c.Value() != 0 {
		t.Fatalf("zero value Value() = %d, want 0", c.Value())
	}
	c.Inc()
	c.Inc()
	if c.Value() != 2 {
		t.Fatalf("Value() = %d, want 2", c.Value())
	}
}

func TestCounterConcurrentInc(t *testing.T) {
	var c Counter
	var wg sync.WaitGroup
	const goroutines, incsEach = 50, 100
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < incsEach; j++ {
				c.Inc()
			}
		}()
	}
	wg.Wait()
	if want := int64(goroutines * incsEach); c.Value() != want {
		t.Fatalf("Value() = %d, want %d", c.Value(), want)
	}
}
