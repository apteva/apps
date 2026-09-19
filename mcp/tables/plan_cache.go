package main

import "sync"

// queryPlanCache stores bounded, value-independent SQL shapes. SQLite still
// binds runtime values, while the batch path avoids rebuilding the same SELECT
// text for repeated table/filter/projection shapes.
const maxQueryPlanEntries = 2048

type queryPlanCache struct {
	mu      sync.Mutex
	entries map[string]string
}

func (c *queryPlanCache) get(key string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	value, ok := c.entries[key]
	return value, ok
}

func (c *queryPlanCache) put(key, value string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = make(map[string]string)
	}
	if len(c.entries) >= maxQueryPlanEntries {
		c.entries = make(map[string]string)
	}
	c.entries[key] = value
}

func (c *queryPlanCache) invalidateTable(tableID int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	prefix := tablePlanPrefix(tableID)
	for key := range c.entries {
		if len(key) >= len(prefix) && key[:len(prefix)] == prefix {
			delete(c.entries, key)
		}
	}
}

func tablePlanPrefix(tableID int64) string {
	return "search:" + formatInt(tableID) + ":"
}

func formatInt(value int64) string {
	if value == 0 {
		return "0"
	}
	negative := value < 0
	if negative {
		value = -value
	}
	buf := [20]byte{}
	i := len(buf)
	for value > 0 {
		i--
		buf[i] = byte('0' + value%10)
		value /= 10
	}
	if negative {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
