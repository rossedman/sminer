package cli

import (
	"bytes"
	"context"
	"io"
	"os"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"github.com/rossedman/sminer/internal/classify"
)

// openPTY returns a pseudo-terminal pair. The slave looks like a TTY to
// isTerminal, so commands take their interactive code paths.
func openPTY(t *testing.T) (master, slave *os.File) {
	t.Helper()
	m, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Fatalf("open /dev/ptmx: %v", err)
	}
	ioctl := func(req, arg uintptr) {
		if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, m.Fd(), req, arg); e != 0 {
			t.Fatalf("ioctl %#x: %v", req, e)
		}
	}
	var unlock int32
	ioctl(syscall.TIOCSPTLCK, uintptr(unsafe.Pointer(&unlock)))
	var n uint32
	ioctl(syscall.TIOCGPTN, uintptr(unsafe.Pointer(&n)))
	s, err := os.OpenFile("/dev/pts/"+strconv.Itoa(int(n)), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		m.Close()
		t.Fatalf("open pty slave: %v", err)
	}
	t.Cleanup(func() { m.Close(); s.Close() })
	return m, s
}

// runPTY runs sminer with stdout, stderr and stdin all on one terminal.
// input is typed before the command starts. It returns the exit code and
// everything the command drew.
func (h *harness) runPTY(input string, argv ...string) (int, string) {
	h.t.Helper()
	return h.runPTYCtx(context.Background(), input, argv...)
}

func (h *harness) runPTYCtx(ctx context.Context, input string, argv ...string) (int, string) {
	h.t.Helper()
	m, s := openPTY(h.t)
	var buf bytes.Buffer
	drained := make(chan struct{})
	go func() {
		_, _ = io.Copy(&buf, m)
		close(drained)
	}()
	if input != "" {
		if _, err := m.WriteString(input); err != nil {
			h.t.Fatal(err)
		}
	}
	code := Run(ctx, argv, Options{
		Stdin: s, Stdout: s, Stderr: s,
		Now:    func() time.Time { return time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC) },
		NewLLM: func(string) classify.LLM { return h.llm },
	})
	s.Close() // the master then reads EOF (EIO) once drained
	<-drained
	return code, strings.ReplaceAll(buf.String(), "\r\n", "\n")
}
