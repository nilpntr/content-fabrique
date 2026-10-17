package sandbox

import "sync"

// boundedBuffer keeps the first max bytes written to it and silently discards the rest, so a chatty process is never
// blocked on a full pipe and never grows our memory without bound.
type boundedBuffer struct {
	mu        sync.Mutex
	buf       []byte
	max       int
	truncated bool
}

func newBoundedBuffer(limit int) *boundedBuffer { return &boundedBuffer{max: limit} }

// Write always reports the full length as written.
func (b *boundedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	room := b.max - len(b.buf)
	if room > len(p) {
		room = len(p)
	}
	if room > 0 {
		b.buf = append(b.buf, p[:room]...)
	}
	if room < len(p) {
		b.truncated = true
	}
	return len(p), nil
}

func (b *boundedBuffer) snapshot() ([]byte, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]byte(nil), b.buf...), b.truncated
}
