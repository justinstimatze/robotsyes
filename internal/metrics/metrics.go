// Package metrics is a small, dependency-free counter shared across
// robots.yes's observability surface. The project's whole dependency
// footprint is two libraries (yaml.v3, x/net) — a handful of atomic
// counters exposed in Prometheus text format doesn't need a client
// library to get there.
package metrics

import "sync/atomic"

// Counter is a thread-safe monotonic counter.
type Counter struct {
	n atomic.Int64
}

// Inc increments the counter by one.
func (c *Counter) Inc() { c.n.Add(1) }

// Value returns the current count.
func (c *Counter) Value() int64 { return c.n.Load() }
