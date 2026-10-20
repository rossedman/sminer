package cli

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

// spinner shows pipeline milestones on stderr. On a terminal the current
// step spins and turns into a check mark when the next one starts or any
// other line is printed; elsewhere each step is one plain line.
type spinner struct {
	a     *app
	mu    sync.Mutex
	msg   string
	start time.Time
	stop  chan struct{}
	done  chan struct{}
}

var spinFrames = []string{"⣾", "⣽", "⣻", "⢿", "⡿", "⣟", "⣯", "⣷"}

// step finishes the running step, if any, and starts msg.
func (s *spinner) step(msg string) {
	a := s.a
	if a.g.quiet {
		return
	}
	if !a.fancyErr() {
		fmt.Fprintf(a.err, "%s %s\n", style(a.theme().Faint, "·"), msg)
		return
	}
	s.finish(true)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.msg, s.start = capitalize(msg), time.Now()
	s.stop, s.done = make(chan struct{}), make(chan struct{})
	go s.run(s.stop, s.done)
}

func (s *spinner) run(stop, done chan struct{}) {
	defer close(done)
	t := time.NewTicker(80 * time.Millisecond)
	defer t.Stop()
	for i := 0; ; i++ {
		s.mu.Lock()
		th := s.a.theme()
		fmt.Fprintf(s.a.err, "\r\x1b[K%s %s", style(th.Heading, spinFrames[i%len(spinFrames)]), s.msg)
		s.mu.Unlock()
		select {
		case <-stop:
			return
		case <-t.C:
		}
	}
}

// pause finishes the running step so another line can be printed.
func (s *spinner) pause() { s.finish(true) }

// outcome replaces the running step's line with its outcome: title in full,
// detail muted after it. Returns false when nothing was spinning.
func (s *spinner) outcome(title, detail string) bool {
	s.mu.Lock()
	running := s.stop != nil
	if running {
		s.msg = title
		if detail != "" {
			s.msg += "  " + style(s.a.theme().Faint, detail)
		}
	}
	s.mu.Unlock()
	if running {
		s.finish(true)
	}
	return running
}

// finish stops the spinner and leaves a check (ok) or a cross behind.
func (s *spinner) finish(ok bool) {
	s.mu.Lock()
	stop, done := s.stop, s.done
	s.stop = nil
	s.mu.Unlock()
	if stop == nil {
		return
	}
	close(stop)
	<-done
	th := s.a.theme()
	mark := style(th.OK, "✓")
	if !ok {
		mark = style(th.Err, "✗")
	}
	el := time.Since(s.start)
	fmt.Fprintf(s.a.err, "\r\x1b[K%s %s %s\n", mark, s.msg, style(th.Faint, "· "+el.Round(time.Millisecond).String()))
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
