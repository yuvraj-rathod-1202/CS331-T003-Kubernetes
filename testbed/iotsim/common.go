package main

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"time"
)

// lineWriter writes one event per line and flushes each line.
type lineWriter struct {
	mu sync.Mutex
	w  *bufio.Writer
}

func newLineWriter(w io.Writer) *lineWriter {
	return &lineWriter{w: bufio.NewWriter(w)}
}

// formatLine builds "<ts_ns> <type> <fields...>" without a trailing newline.
func formatLine(ts int64, typ string, fields ...string) string {
	var b strings.Builder
	b.WriteString(strconv.FormatInt(ts, 10))
	b.WriteByte(' ')
	b.WriteString(typ)
	for _, f := range fields {
		b.WriteByte(' ')
		b.WriteString(f)
	}
	return b.String()
}

// emitAt writes a line with an explicit timestamp.
func (l *lineWriter) emitAt(ts int64, typ string, fields ...string) {
	line := formatLine(ts, typ, fields...)
	l.mu.Lock()
	defer l.mu.Unlock()
	_, _ = l.w.WriteString(line)
	_ = l.w.WriteByte('\n')
	_ = l.w.Flush()
}

// emit writes a line stamped with the current time.
func (l *lineWriter) emit(typ string, fields ...string) {
	l.emitAt(time.Now().UnixNano(), typ, fields...)
}

// sanitizeReason replaces whitespace with '_' so the reason is one field.
func sanitizeReason(s string) string {
	s = strings.Join(strings.Fields(s), "_")
	if s == "" {
		return "unknown"
	}
	return s
}

func clientID(group string, i int) string {
	return fmt.Sprintf("%s-s%02d", group, i)
}

// makePayload builds "<clientid> <seq> <sent_ns> <qos>".
func makePayload(id string, seq uint64, sentNs int64, qos int) string {
	return fmt.Sprintf("%s %d %d %d", id, seq, sentNs, qos)
}

type payload struct {
	ClientID string
	Seq      uint64
	SentNs   int64
	QoS      int
}

func parsePayload(s string) (payload, error) {
	f := strings.Fields(s)
	if len(f) != 4 {
		return payload{}, fmt.Errorf("want 4 fields, got %d", len(f))
	}
	seq, err := strconv.ParseUint(f[1], 10, 64)
	if err != nil {
		return payload{}, fmt.Errorf("seq: %w", err)
	}
	sent, err := strconv.ParseInt(f[2], 10, 64)
	if err != nil {
		return payload{}, fmt.Errorf("sent_ns: %w", err)
	}
	q, err := strconv.Atoi(f[3])
	if err != nil {
		return payload{}, fmt.Errorf("qos: %w", err)
	}
	return payload{f[0], seq, sent, q}, nil
}
