package proccompose

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/ang-ee/angee-operator/internal/runtime"
)

type recordingRunner struct {
	name string
	args []string
}

func TestEnsureAvailableResolvesFromPath(t *testing.T) {
	backend := Backend{
		LookupPath: func(name string) (string, error) {
			if name == "process-compose" {
				return "/usr/bin/process-compose", nil
			}
			return "", errors.New("not found")
		},
	}
	if err := backend.EnsureAvailable(context.Background()); err != nil {
		t.Fatalf("EnsureAvailable() error = %v", err)
	}
}

func TestEnsureAvailableErrorsWhenMissing(t *testing.T) {
	// LookupPath fails for process-compose and go, so bootstrap's $GOPATH/bin
	// fallback cannot resolve it either: the result is the actionable error.
	backend := Backend{
		LookupPath: func(string) (string, error) {
			return "", errors.New("not found")
		},
	}
	err := backend.EnsureAvailable(context.Background())
	if err == nil || !strings.Contains(err.Error(), "process-compose is required") {
		t.Fatalf("EnsureAvailable() error = %v, want process-compose required", err)
	}
}

func (r *recordingRunner) Run(_ context.Context, _ string, _ []string, name string, args ...string) ([]byte, error) {
	r.name = name
	r.args = append([]string(nil), args...)
	return nil, nil
}

func TestBackendUpCommand(t *testing.T) {
	runner := &recordingRunner{}
	backend := Backend{Runner: runner}
	err := backend.Up(context.Background(), runtime.Target{Root: "/stack", Services: []string{"web"}, ControlPort: 10002})
	if err != nil {
		t.Fatalf("Up() error = %v", err)
	}
	want := []string{"-f", "/stack/process-compose.yaml", "--address", "127.0.0.1", "--port", "10002", "up", "-d", "--tui=false", "web"}
	if runner.name != "process-compose" || !reflect.DeepEqual(runner.args, want) {
		t.Fatalf("command = %s %v, want process-compose %v", runner.name, runner.args, want)
	}
}

func TestBackendDownUsesControlPort(t *testing.T) {
	runner := &recordingRunner{}
	backend := Backend{Runner: runner}
	err := backend.Down(context.Background(), runtime.Target{Root: "/stack", ControlPort: 10003})
	if err != nil {
		t.Fatalf("Down() error = %v", err)
	}
	want := []string{"--address", "127.0.0.1", "--port", "10003", "down"}
	if runner.name != "process-compose" || !reflect.DeepEqual(runner.args, want) {
		t.Fatalf("command = %s %v, want process-compose %v", runner.name, runner.args, want)
	}
}

type stubListRunner struct {
	args   []string
	output []byte
	err    error
}

func (r *stubListRunner) Run(_ context.Context, _ string, _ []string, _ string, args ...string) ([]byte, error) {
	r.args = append([]string(nil), args...)
	return r.output, r.err
}

func TestBackendStatusParsesProcessList(t *testing.T) {
	const payload = `[
	{"name":"build-watch","status":"Running","is_running":true,"exit_code":0,"is_ready":"-"},
	{"name":"web","status":"Running","is_running":true,"exit_code":0,"is_ready":"Ready"},
	{"name":"migrate","status":"Completed","is_running":false,"exit_code":0,"is_ready":"-"}
]`
	runner := &stubListRunner{output: []byte(payload)}
	backend := Backend{Runner: runner}
	got, err := backend.Status(context.Background(), runtime.StatusRequest{Root: "/stack", ControlPort: 10004})
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	wantArgs := []string{"--address", "127.0.0.1", "--port", "10004", "list", "-o", "json"}
	if !reflect.DeepEqual(runner.args, wantArgs) {
		t.Fatalf("args = %v, want %v", runner.args, wantArgs)
	}
	want := []runtime.ServiceStatus{
		{Name: "build-watch", Runtime: "local", State: "running"},
		{Name: "web", Runtime: "local", State: "running", Health: "healthy"},
		{Name: "migrate", Runtime: "local", State: "completed"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("statuses = %#v, want %#v", got, want)
	}
}

func TestBackendStatusSwallowsErrors(t *testing.T) {
	runner := &stubListRunner{err: errors.New("supervisor offline")}
	backend := Backend{Runner: runner}
	got, err := backend.Status(context.Background(), runtime.StatusRequest{Root: "/stack", ControlPort: 10005})
	if err != nil {
		t.Fatalf("Status() error = %v, want nil", err)
	}
	if got != nil {
		t.Fatalf("statuses = %v, want nil", got)
	}
}
