package link

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"handloom/internal/api"
	"handloom/internal/client"
)

// The call journal: the link's own record of every tool call, kept on this computer. A line is written and
// synced before a call is forwarded to the add-on, another when it ends, and a denied call has a line too.
// A shipper sends the lines the hub has not accepted yet. Lines are api.AddonCall values and nothing else:
// no field can hold what an agent asked or what a tool answered, which stay on this computer.

const (
	maxShipBatch  = 200     // the most lines the hub takes in one report
	maxShipBytes  = 1 << 20 // the most the shipper reads at a time
	journalRotate = 8 << 20 // a journal that is fully shipped and larger than this is moved aside
	shipLogEvery  = 5 * time.Minute
	shipBatchesAt = 50 // batches sent in one run of the ladder, so one run cannot go on for ever
)

type callJournal struct {
	mu   sync.Mutex
	path string // the journal; <path>.sent holds how many bytes of it the hub has accepted
}

func (l *Link) callJournal() *callJournal {
	l.jmu.Lock()
	defer l.jmu.Unlock()
	if l.journal == nil {
		p := l.opt.Journal
		if p == "" {
			p = filepath.Join(client.Home(), "addon-calls.jsonl")
		}
		l.journal = &callJournal{path: p}
	}
	return l.journal
}

// append writes one line and syncs it to disk before it returns.
func (j *callJournal) append(e api.AddonCall) error {
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(j.path), 0o700); err != nil {
		return err
	}
	_, statErr := os.Stat(j.path)
	f, err := os.OpenFile(j.path, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	// A line cut short by a crash must not swallow the next one.
	if st, err := f.Stat(); err == nil && st.Size() > 0 {
		last := make([]byte, 1)
		if rf, err := os.Open(j.path); err == nil {
			_, rerr := rf.ReadAt(last, st.Size()-1)
			rf.Close()
			if rerr == nil && last[0] != '\n' {
				b = append([]byte{'\n'}, b...)
			}
		}
	}
	if _, err := f.Write(append(b, '\n')); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if statErr != nil { // a new file: make its place in the folder durable too
		if d, err := os.Open(filepath.Dir(j.path)); err == nil {
			d.Sync()
			d.Close()
		}
	}
	return nil
}

func (j *callJournal) sentPath() string { return j.path + ".sent" }

// offset is how far into the journal the hub has accepted. A damaged or impossible value counts as zero:
// lines are sent again, and the hub skips what it already stored.
func (j *callJournal) offset(size int64) int64 {
	b, err := os.ReadFile(j.sentPath())
	if err != nil {
		return 0
	}
	n, err := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
	if err != nil || n < 0 || n > size {
		return 0
	}
	return n
}

func (j *callJournal) markSent(n int64) error {
	if b, err := os.ReadFile(j.sentPath()); err == nil && strings.TrimSpace(string(b)) == strconv.FormatInt(n, 10) {
		return nil
	}
	tmp := j.sentPath() + ".tmp"
	if err := os.WriteFile(tmp, []byte(strconv.FormatInt(n, 10)+"\n"), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, j.sentPath())
}

// unsent reads the next lines the hub has not accepted: at most max of them, and the offset just past them.
// Lines that are not a call (a torn line, an edited file) are passed over and counted in bad.
func (j *callJournal) unsent(max int) (calls []api.AddonCall, next int64, bad int, err error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	f, err := os.Open(j.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, 0, 0, nil
	}
	if err != nil {
		return nil, 0, 0, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, 0, 0, err
	}
	next = j.offset(st.Size())
	buf := make([]byte, maxShipBytes)
	n, err := f.ReadAt(buf, next)
	if err != nil && n == 0 {
		return nil, next, 0, nil // nothing past the offset
	}
	buf = buf[:n]
	for len(calls) < max {
		i := bytes.IndexByte(buf, '\n')
		if i < 0 {
			break // the rest is a line still being written, or longer than we read
		}
		line := bytes.TrimSpace(buf[:i])
		buf = buf[i+1:]
		next += int64(i) + 1
		if len(line) == 0 {
			continue
		}
		var e api.AddonCall
		if json.Unmarshal(line, &e) != nil {
			bad++
			continue
		}
		calls = append(calls, e)
	}
	return calls, next, bad, nil
}

// settle moves a journal that is fully shipped and large out of the way, so it does not grow for ever.
func (j *callJournal) settle() {
	j.mu.Lock()
	defer j.mu.Unlock()
	st, err := os.Stat(j.path)
	if err != nil || st.Size() < journalRotate || j.offset(st.Size()) != st.Size() {
		return
	}
	if os.Rename(j.path, j.path+".old") == nil {
		j.markSent(0)
	}
}

// runAddonCalls ships the journal lines the hub has not accepted. A line counts as sent only after the hub
// took it. When the hub cannot be reached nothing is lost and nothing waits: the lines stay in the journal and
// go with the next run. A call never depends on this.
func (l *Link) runAddonCalls(ctx context.Context) {
	j := l.callJournal()
	for i := 0; i < shipBatchesAt; i++ {
		calls, next, bad, err := j.unsent(maxShipBatch)
		if err != nil {
			l.shipLog("the call journal cannot be read: %v", err)
			return
		}
		if bad > 0 {
			l.opt.Log.Printf("the call journal has %d line(s) that are not calls; they are passed over", bad)
		}
		if len(calls) == 0 {
			j.mu.Lock()
			j.markSent(next) // lines passed over (or none) are settled too
			j.mu.Unlock()
			j.settle()
			return
		}
		err = l.hub.Do(ctx, "POST", "/v1/device/addon-calls", api.AddonCallReport{Calls: calls}, nil)
		var ce *client.Error
		switch {
		case err == nil:
		case errors.As(err, &ce) && ce.Status == 400:
			// The hub refuses a whole batch for one bad line. Send them one at a time, so one line the
			// hub will never take (a spawn it has forgotten) does not hold back the rest.
			if !l.shipEach(ctx, calls) {
				return
			}
		default:
			if ctx.Err() == nil {
				l.shipLog("the tool calls could not be reported to the hub (they are kept and sent later): %v", err)
			}
			return
		}
		j.mu.Lock()
		err = j.markSent(next)
		j.mu.Unlock()
		if err != nil {
			l.shipLog("the call journal's sent mark could not be written: %v", err)
			return
		}
	}
}

// shipEach sends lines one by one. A line the hub refuses is dropped with a note in the link's log; false
// means the hub could not be reached and the batch must be tried again as it was.
func (l *Link) shipEach(ctx context.Context, calls []api.AddonCall) bool {
	for _, c := range calls {
		err := l.hub.Do(ctx, "POST", "/v1/device/addon-calls", api.AddonCallReport{Calls: []api.AddonCall{c}}, nil)
		var ce *client.Error
		switch {
		case err == nil:
		case errors.As(err, &ce) && ce.Status == 400:
			l.opt.Log.Printf("the hub refused a journal line (%s %s.%s, %s) and it is dropped: %v", c.CallID, c.Addon, c.Tool, c.Phase, err)
		default:
			if ctx.Err() == nil {
				l.shipLog("the tool calls could not be reported to the hub (they are kept and sent later): %v", err)
			}
			return false
		}
	}
	return true
}

// shipLog says why shipping failed, at most once in a while: an outage would otherwise fill the log.
func (l *Link) shipLog(format string, args ...any) {
	l.jmu.Lock()
	now := l.opt.Now()
	due := l.shipLogAt.IsZero() || now.Sub(l.shipLogAt) >= shipLogEvery
	if due {
		l.shipLogAt = now
	}
	l.jmu.Unlock()
	if due {
		l.opt.Log.Print(fmt.Sprintf(format, args...))
	}
}
