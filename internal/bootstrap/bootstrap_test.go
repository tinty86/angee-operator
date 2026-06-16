package bootstrap

import (
	"bytes"
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
)

func TestRunNoopsWhenToolsPresent(t *testing.T) {
	commands := &recordingCommands{}
	opts := fakeOptions(map[string]bool{}, commands)
	report, err := Run(context.Background(), opts)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if report.Summary.Present != len(MandatoryTools()) {
		t.Fatalf("present = %d, want %d", report.Summary.Present, len(MandatoryTools()))
	}
	if len(commands.installs) != 0 {
		t.Fatalf("install commands = %v, want none", commands.installs)
	}
}

func TestRunInstallsProcessComposeWithGoInstall(t *testing.T) {
	commands := &recordingCommands{}
	missing := map[string]bool{"process-compose": true}
	opts := Options{
		OS: "darwin",
		LookupPath: func(name string) (string, error) {
			if name == "process-compose" && missing[name] {
				return "", errors.New("not found")
			}
			return "/fake/" + name, nil
		},
		RunCommand: commands.run(func(command Command) {
			if command.Name == "go" && reflect.DeepEqual(command.Args, []string{"install", ProcessComposeInstallPackage}) {
				missing["process-compose"] = false
			}
		}),
		Stderr: io.Discard,
	}

	report, err := Run(context.Background(), opts)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if report.Summary.Installed != 1 {
		t.Fatalf("installed = %d, want 1", report.Summary.Installed)
	}
	want := []Command{{Name: "go", Args: []string{"install", ProcessComposeInstallPackage}}}
	if !reflect.DeepEqual(commands.installs, want) {
		t.Fatalf("install commands = %#v, want %#v", commands.installs, want)
	}
}

func TestRunDryRunPlansMissingUVWithBrew(t *testing.T) {
	opts := fakeOptions(map[string]bool{"uv": true}, &recordingCommands{})
	opts.DryRun = true
	report, err := Run(context.Background(), opts)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if report.Summary.DryRun != 1 {
		t.Fatalf("dry_run = %d, want 1", report.Summary.DryRun)
	}
	var uv ToolResult
	for _, tool := range report.Tools {
		if tool.Name == "uv" {
			uv = tool
			break
		}
	}
	want := []Command{{Name: "brew", Args: []string{"install", "uv"}}}
	if uv.Installer != "Homebrew" || !reflect.DeepEqual(uv.Commands, want) {
		t.Fatalf("uv result = %#v, want Homebrew command %#v", uv, want)
	}
}

func TestRunReportsManualWhenNoInstallerExists(t *testing.T) {
	commands := &recordingCommands{}
	opts := Options{
		OS: "plan9",
		LookupPath: func(name string) (string, error) {
			if name == "docker" || name == "brew" {
				return "", errors.New("not found")
			}
			return "/fake/" + name, nil
		},
		RunCommand: commands.run(nil),
		Stderr:     io.Discard,
	}

	report, err := Run(context.Background(), opts)
	if err == nil {
		t.Fatal("Run() error is nil")
	}
	var bootErr Error
	if !errors.As(err, &bootErr) || bootErr.Manual != 1 {
		t.Fatalf("error = %#v, want one manual install", err)
	}
	if report.Summary.Manual != 1 {
		t.Fatalf("manual = %d, want 1", report.Summary.Manual)
	}
	if len(commands.installs) != 0 {
		t.Fatalf("install commands = %#v, want none", commands.installs)
	}
}

func fakeOptions(missing map[string]bool, commands *recordingCommands) Options {
	return Options{
		OS: "darwin",
		LookupPath: func(name string) (string, error) {
			if missing[name] {
				return "", errors.New("not found")
			}
			return "/fake/" + name, nil
		},
		RunCommand: commands.run(nil),
		Stderr:     io.Discard,
	}
}

type recordingCommands struct {
	installs []Command
}

func (r *recordingCommands) run(afterInstall func(Command)) func(context.Context, Command, io.Writer, io.Writer) error {
	return func(_ context.Context, command Command, stdout io.Writer, _ io.Writer) error {
		if isVersionCommand(command) {
			_, _ = io.WriteString(stdout, command.Name+" version 1.0\n")
			return nil
		}
		r.installs = append(r.installs, command)
		if afterInstall != nil {
			afterInstall(command)
		}
		return nil
	}
}

func isVersionCommand(command Command) bool {
	if len(command.Args) == 0 {
		return false
	}
	switch {
	case command.Args[0] == "--version":
		return true
	case command.Args[0] == "version":
		return true
	case len(command.Args) == 2 && command.Args[0] == "env":
		return true
	default:
		return false
	}
}

func TestWriteReportHasUsefulHumanOutput(t *testing.T) {
	var out bytes.Buffer
	report := Report{
		Tools: []ToolResult{
			{Name: "git", Status: StatusPresent, Version: "git version 2.0"},
			{Name: "docker", Status: StatusManual, Detail: "not found on PATH", Hint: "Install Docker."},
		},
		Summary: Summary{Present: 1, Manual: 1},
	}
	if err := WriteReport(&out, report); err != nil {
		t.Fatalf("WriteReport() error = %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "PRESENT") || !strings.Contains(got, "git") || !strings.Contains(got, "MANUAL") || !strings.Contains(got, "docker") || !strings.Contains(got, "Install Docker.") {
		t.Fatalf("human report missing expected content:\n%s", got)
	}
}
