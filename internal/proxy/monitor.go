// Performance marks, ported from src/utils/performance.js: a monitor records
// elapsed milliseconds (integer, like Date.now() - startTime) under named
// marks and serializes them in insertion order, matching
// JSON.stringify(monitor.getMetrics()).
//
// Xget-Go - a single-binary port of xixu-me/Xget (AGPL-3.0-or-later).
package proxy

import (
	"strconv"
	"strings"
	"time"
)

// performanceMonitor tracks named timing marks for one request.
type performanceMonitor struct {
	start time.Time
	marks map[string]int64
	order []string
}

func newPerformanceMonitor() *performanceMonitor {
	return &performanceMonitor{start: time.Now(), marks: make(map[string]int64)}
}

// mark records the elapsed milliseconds since the monitor started. Re-marking
// an existing name updates its value while keeping the original position, the
// same semantics as Map#set.
func (m *performanceMonitor) mark(name string) {
	if _, ok := m.marks[name]; !ok {
		m.order = append(m.order, name)
	}
	m.marks[name] = time.Since(m.start).Milliseconds()
}

// json renders the marks exactly like JSON.stringify of the upstream marks
// object: insertion-ordered keys with integer millisecond values.
func (m *performanceMonitor) json() string {
	var b strings.Builder
	b.WriteByte('{')
	for i, name := range m.order {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.Quote(name))
		b.WriteByte(':')
		b.WriteString(strconv.FormatInt(m.marks[name], 10))
	}
	b.WriteByte('}')
	return b.String()
}
