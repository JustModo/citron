package sandbox

import "bytes"

// limitWriter buffers at most max bytes and drops the rest. It never fails a write:
// refusing would block the writer in the kernel and turn an output bomb into a hang.
// onExceed fires once, on the first dropped byte, so the caller can kill the process.
type limitWriter struct {
	buf       bytes.Buffer
	max       int64
	truncated bool
	onExceed  func()
}

func newLimitWriter(max int64, onExceed func()) *limitWriter {
	return &limitWriter{max: max, onExceed: onExceed}
}

func (w *limitWriter) Write(p []byte) (int, error) {
	room := w.max - int64(w.buf.Len())
	if int64(len(p)) <= room {
		w.buf.Write(p)
		return len(p), nil
	}
	if room > 0 {
		w.buf.Write(p[:room])
	}
	if !w.truncated {
		w.truncated = true
		if w.onExceed != nil {
			w.onExceed()
		}
	}
	return len(p), nil
}

func (w *limitWriter) Bytes() []byte   { return w.buf.Bytes() }
func (w *limitWriter) Truncated() bool { return w.truncated }
