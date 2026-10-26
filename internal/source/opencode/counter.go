// SPDX-License-Identifier: AGPL-3.0-only

package ocsource

import "sort"

// Counter replicates Python's collections.Counter insertion order.
//
// MostCommon mirrors Counter.most_common(): descending count with ties in
// first-insertion order. Several report sections (sessionHealth.events,
// finishes, toolErrors, versions) are order-sensitive because the audit
// compares the emitted lists element by element.
type Counter struct {
	order  []string
	counts map[string]int64
}

// NewCounter returns an empty ordered counter.
func NewCounter() Counter {
	return Counter{counts: map[string]int64{}}
}

// Inc adds one to key.
func (c *Counter) Inc(key string) { c.Add(key, 1) }

// Add adds n to key, registering the key on first use.
func (c *Counter) Add(key string, n int64) {
	if c.counts == nil {
		c.counts = map[string]int64{}
	}
	if _, ok := c.counts[key]; !ok {
		c.order = append(c.order, key)
	}
	c.counts[key] += n
}

// Get returns the count for key (0 when absent).
func (c *Counter) Get(key string) int64 { return c.counts[key] }

// Total sums every count.
func (c *Counter) Total() int64 {
	var total int64
	for _, count := range c.counts {
		total += count
	}
	return total
}

// Len is the number of distinct keys.
func (c *Counter) Len() int { return len(c.order) }

// Keys returns keys in insertion order.
func (c *Counter) Keys() []string {
	out := make([]string, len(c.order))
	copy(out, c.order)
	return out
}

// CounterItem is one (key, count) pair.
type CounterItem struct {
	Key   string
	Count int64
}

// Items returns the pairs in first-insertion order (Python dict order).
func (c *Counter) Items() []CounterItem {
	out := make([]CounterItem, 0, len(c.order))
	for _, key := range c.order {
		out = append(out, CounterItem{Key: key, Count: c.counts[key]})
	}
	return out
}

// MostCommon returns pairs sorted by descending count, ties in insertion
// order: the exact semantics of Counter.most_common().
func (c *Counter) MostCommon() []CounterItem {
	items := c.Items()
	sort.SliceStable(items, func(i, j int) bool { return items[i].Count > items[j].Count })
	return items
}
