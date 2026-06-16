package service

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/ang-ee/angee-operator/internal/manifest"
	runtimex "github.com/ang-ee/angee-operator/internal/runtime"
)

// TestGuardDevSink keeps the colouring contract: a real terminal (*os.File) is
// passed through so exec hands the child the TTY fd, while any other sink is
// wrapped for safe concurrent writes.
func TestGuardDevSink(t *testing.T) {
	if got := guardDevSink(os.Stdout); got != os.Stdout {
		t.Fatalf("guardDevSink(*os.File) = %T, want the file unwrapped", got)
	}
	var buf bytes.Buffer
	if _, ok := guardDevSink(&buf).(*syncWriter); !ok {
		t.Fatalf("guardDevSink(non-file) did not wrap in *syncWriter")
	}
}

// TestSyncWriterSerializesConcurrentWrites guards the dev-stream race fix: the
// two `angee dev` backends write to the same sink concurrently, so syncWriter
// must serialize those writes. The underlying bytes.Buffer is not safe for
// concurrent use, so `go test -race` fails here if the mutex is ever removed.
func TestSyncWriterSerializesConcurrentWrites(t *testing.T) {
	var buf bytes.Buffer
	w := &syncWriter{w: &buf}

	const writers, perWriter = 8, 100
	line := []byte("agent-demo-agent | starting\n")
	var wg sync.WaitGroup
	for range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range perWriter {
				if _, err := w.Write(line); err != nil {
					t.Errorf("Write: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()

	if got, want := buf.Len(), writers*perWriter*len(line); got != want {
		t.Fatalf("buffered %d bytes, want %d", got, want)
	}
}

func TestStackDevForegroundEnsuresLocalRuntimeBeforeBuildOrStart(t *testing.T) {
	root := t.TempDir()
	writeDevPreflightStack(t, root)

	events := &devOrderRecorder{}
	composeBackend := &devOrderBackend{name: "compose", events: events}
	procBackend := &devProcessBackend{
		devOrderBackend: &devOrderBackend{name: "process", events: events},
	}
	platform, err := NewWithBackends(root, composeBackend, procBackend)
	if err != nil {
		t.Fatalf("NewWithBackends() error = %v", err)
	}

	if err := platform.StackDevForeground(context.Background(), true, io.Discard, io.Discard); err != nil {
		t.Fatalf("StackDevForeground() error = %v", err)
	}

	got := events.snapshot()
	if len(got) == 0 || got[0] != "process:ensure" {
		t.Fatalf("events = %v, want process ensure before any runtime work", got)
	}
	for _, event := range []string{"compose:build", "compose:up-foreground", "process:up-foreground"} {
		if idx := eventIndex(got, event); idx <= 0 {
			t.Fatalf("events = %v, want %s after process ensure", got, event)
		}
	}
}

func TestStackDevForegroundEnsuresLocalRuntimeBeforeOpenBaoBootstrap(t *testing.T) {
	root := t.TempDir()
	writeDevPreflightStack(t, root, withOpenBaoBootstrap)

	events := &devOrderRecorder{}
	composeBackend := &devOrderBackend{name: "compose", events: events}
	procBackend := &devProcessBackend{
		devOrderBackend: &devOrderBackend{name: "process", events: events},
	}
	platform, err := NewWithBackends(root, composeBackend, procBackend)
	if err != nil {
		t.Fatalf("NewWithBackends() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = platform.StackDevForeground(ctx, true, io.Discard, io.Discard)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("StackDevForeground() error = %v, want context.Canceled", err)
	}
	got := events.snapshot()
	if len(got) < 2 || got[0] != "process:ensure" || got[1] != "compose:up-foreground" {
		t.Fatalf("events = %v, want process ensure before OpenBao bootstrap compose start", got)
	}
}

func TestStackDevForegroundStopsWhenLocalRuntimePreflightFails(t *testing.T) {
	root := t.TempDir()
	writeDevPreflightStack(t, root, withOpenBaoBootstrap)

	wantErr := errors.New("process-compose missing")
	events := &devOrderRecorder{}
	composeBackend := &devOrderBackend{name: "compose", events: events}
	procBackend := &devProcessBackend{
		devOrderBackend: &devOrderBackend{name: "process", events: events},
		ensureErr:       wantErr,
	}
	platform, err := NewWithBackends(root, composeBackend, procBackend)
	if err != nil {
		t.Fatalf("NewWithBackends() error = %v", err)
	}

	err = platform.StackDevForeground(context.Background(), true, io.Discard, io.Discard)
	if !errors.Is(err, wantErr) {
		t.Fatalf("StackDevForeground() error = %v, want %v", err, wantErr)
	}
	if got := strings.Join(events.snapshot(), ","); got != "process:ensure" {
		t.Fatalf("events = %s, want only process ensure", got)
	}
}

func TestStackDevEnsuresLocalRuntimeBeforeDetachedStart(t *testing.T) {
	root := t.TempDir()
	writeDevPreflightStack(t, root)

	events := &devOrderRecorder{}
	composeBackend := &devOrderBackend{name: "compose", events: events}
	procBackend := &devProcessBackend{
		devOrderBackend: &devOrderBackend{name: "process", events: events},
	}
	platform, err := NewWithBackends(root, composeBackend, procBackend)
	if err != nil {
		t.Fatalf("NewWithBackends() error = %v", err)
	}

	if err := platform.StackDev(context.Background(), true); err != nil {
		t.Fatalf("StackDev() error = %v", err)
	}
	got := events.snapshot()
	if len(got) == 0 || got[0] != "process:ensure" {
		t.Fatalf("events = %v, want process ensure before detached runtime work", got)
	}
	for _, event := range []string{"compose:up", "process:up"} {
		if idx := eventIndex(got, event); idx <= 0 {
			t.Fatalf("events = %v, want %s after process ensure", got, event)
		}
	}
}

type devOrderRecorder struct {
	mu     sync.Mutex
	events []string
}

func (r *devOrderRecorder) add(event string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, event)
}

func (r *devOrderRecorder) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.events...)
}

type devOrderBackend struct {
	name   string
	events *devOrderRecorder
}

func (b *devOrderBackend) record(action string) {
	b.events.add(b.name + ":" + action)
}

func (b *devOrderBackend) Build(context.Context, runtimex.Target) error {
	b.record("build")
	return nil
}

func (b *devOrderBackend) Up(context.Context, runtimex.Target) error {
	b.record("up")
	return nil
}

func (b *devOrderBackend) UpForeground(context.Context, runtimex.Target, io.Writer, io.Writer) error {
	b.record("up-foreground")
	return nil
}

func (b *devOrderBackend) Down(context.Context, runtimex.Target) error {
	b.record("down")
	return nil
}

func (b *devOrderBackend) Start(context.Context, runtimex.Target) error {
	b.record("start")
	return nil
}

func (b *devOrderBackend) Stop(context.Context, runtimex.Target) error {
	b.record("stop")
	return nil
}

func (b *devOrderBackend) Restart(context.Context, runtimex.Target) error {
	b.record("restart")
	return nil
}

func (b *devOrderBackend) Logs(context.Context, runtimex.LogsRequest) (<-chan string, error) {
	ch := make(chan string)
	close(ch)
	return ch, nil
}

func (b *devOrderBackend) Status(context.Context, runtimex.StatusRequest) ([]runtimex.ServiceStatus, error) {
	return nil, nil
}

type devProcessBackend struct {
	*devOrderBackend
	ensureErr error
}

func (b *devProcessBackend) EnsureAvailable(context.Context) error {
	b.record("ensure")
	return b.ensureErr
}

type devPreflightStackOption func(*manifest.Stack)

func writeDevPreflightStack(t *testing.T, root string, opts ...devPreflightStackOption) {
	t.Helper()
	stack := &manifest.Stack{
		Name: "dev-preflight",
		Services: map[string]manifest.Service{
			"edge": {
				Runtime: manifest.RuntimeContainer,
				Image:   "nginx:latest",
			},
			"web": {
				Runtime: manifest.RuntimeLocal,
				Command: []string{"echo", "web"},
			},
		},
	}
	for _, opt := range opts {
		opt(stack)
	}
	if err := manifest.SaveFile(manifest.Path(root), stack); err != nil {
		t.Fatalf("SaveFile() error = %v", err)
	}
}

func withOpenBaoBootstrap(stack *manifest.Stack) {
	stack.SecretsBackend = manifest.SecretsBackend{
		Type:    "openbao",
		Address: "",
	}
	stack.Services["openbao"] = manifest.Service{
		Runtime: manifest.RuntimeContainer,
		Image:   "openbao/openbao:latest",
	}
}

func eventIndex(events []string, event string) int {
	for i, candidate := range events {
		if candidate == event {
			return i
		}
	}
	return -1
}
