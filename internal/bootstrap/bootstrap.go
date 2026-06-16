// Package bootstrap installs the host tools required by the default Angee dev
// stack.
package bootstrap

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// ProcessComposeInstallPackage is the Go package used to install
// process-compose when it is missing.
const ProcessComposeInstallPackage = "github.com/f1bonacc1/process-compose@latest"

// Status is the per-tool bootstrap status.
type Status string

const (
	StatusPresent   Status = "present"
	StatusInstalled Status = "installed"
	StatusDryRun    Status = "dry-run"
	StatusManual    Status = "manual"
	StatusFailed    Status = "failed"
)

// Tool describes a mandatory host command.
type Tool struct {
	Name        string   `json:"name"`
	VersionArgs []string `json:"version_args"`
	Hint        string   `json:"hint,omitempty"`
}

// Command is an argv-form command executed by bootstrap installers.
type Command struct {
	Name string   `json:"name"`
	Args []string `json:"args,omitempty"`
}

// ToolResult reports the outcome for one mandatory tool.
type ToolResult struct {
	Name      string    `json:"name"`
	Status    Status    `json:"status"`
	Path      string    `json:"path,omitempty"`
	Version   string    `json:"version,omitempty"`
	Installer string    `json:"installer,omitempty"`
	Commands  []Command `json:"commands,omitempty"`
	Detail    string    `json:"detail,omitempty"`
	Hint      string    `json:"hint,omitempty"`
}

// Summary aggregates a bootstrap run.
type Summary struct {
	Present   int `json:"present"`
	Installed int `json:"installed"`
	DryRun    int `json:"dry_run"`
	Manual    int `json:"manual"`
	Failed    int `json:"failed"`
}

// Report is the complete bootstrap result.
type Report struct {
	Tools   []ToolResult `json:"tools"`
	Summary Summary      `json:"summary"`
}

// Options configures a bootstrap run.
type Options struct {
	Stdout io.Writer
	Stderr io.Writer
	DryRun bool

	OS         string
	LookupPath func(string) (string, error)
	RunCommand func(context.Context, Command, io.Writer, io.Writer) error
}

// Error reports an incomplete bootstrap.
type Error struct {
	Manual int
	Failed int
}

func (e Error) Error() string {
	parts := []string{}
	if e.Manual > 0 {
		parts = append(parts, fmt.Sprintf("%d manual install(s)", e.Manual))
	}
	if e.Failed > 0 {
		parts = append(parts, fmt.Sprintf("%d failed install(s)", e.Failed))
	}
	return "bootstrap incomplete: " + strings.Join(parts, ", ")
}

// MandatoryTools returns the commands needed by the default Angee development
// host.
func MandatoryTools() []Tool {
	tools := []Tool{
		{Name: "git", VersionArgs: []string{"--version"}, Hint: "Required for git Sources and Workspace branches."},
		{Name: "go", VersionArgs: []string{"version"}, Hint: "Required to install process-compose when it is missing."},
		{Name: "uv", VersionArgs: []string{"--version"}, Hint: "Required by the bundled Django dev stack."},
		{Name: "node", VersionArgs: []string{"--version"}, Hint: "Required by the bundled React/Vite dev stack."},
		{Name: "pnpm", VersionArgs: []string{"--version"}, Hint: "Required by the bundled React/Vite dev stack."},
		{Name: "npx", VersionArgs: []string{"--version"}, Hint: "Required by the bundled playwright-mcp service."},
		{Name: "docker", VersionArgs: []string{"--version"}, Hint: "Required for container runtime Services."},
		{Name: "process-compose", VersionArgs: []string{"--version"}, Hint: "Required for local-process runtime Services."},
	}
	return append([]Tool(nil), tools...)
}

// Check reports mandatory host tools without installing anything.
func Check(ctx context.Context, opts Options) Report {
	opts = opts.withDefaults()
	var report Report
	for _, tool := range MandatoryTools() {
		report.add(checkTool(ctx, opts, tool))
	}
	return report
}

// Run installs any missing mandatory host tools it knows how to install.
func Run(ctx context.Context, opts Options) (Report, error) {
	opts = opts.withDefaults()
	var report Report
	for _, tool := range MandatoryTools() {
		report.add(ensureTool(ctx, opts, tool))
	}
	if report.Summary.Manual > 0 || report.Summary.Failed > 0 {
		return report, Error{Manual: report.Summary.Manual, Failed: report.Summary.Failed}
	}
	return report, nil
}

// LookupProcessCompose resolves the process-compose binary, checking PATH first
// and then $GOPATH/bin. It returns the resolved path, or an error whose message
// points at `angee bootstrap`. It is the single source of truth for locating
// process-compose, shared by the process-compose runtime backend and
// bootstrap's own checks.
func LookupProcessCompose(ctx context.Context, opts Options) (string, error) {
	opts = opts.withDefaults()
	if path, err := opts.LookupPath("process-compose"); err == nil {
		return path, nil
	}
	if path, err := goBinProcessCompose(ctx, opts); err == nil {
		return path, nil
	}
	return "", fmt.Errorf("process-compose is required; run `angee bootstrap` or install it with `go install %s`", ProcessComposeInstallPackage)
}

// WriteReport writes a human-readable bootstrap report.
func WriteReport(w io.Writer, report Report) error {
	if _, err := fmt.Fprintln(w, "angee bootstrap"); err != nil {
		return err
	}
	for _, tool := range report.Tools {
		detail := tool.Version
		if detail == "" {
			detail = tool.Detail
		}
		if detail == "" {
			detail = tool.Path
		}
		if _, err := fmt.Fprintf(w, "%-8s %-16s %s\n", strings.ToUpper(string(tool.Status)), tool.Name, detail); err != nil {
			return err
		}
		if tool.Installer != "" {
			if _, err := fmt.Fprintf(w, "         installer: %s\n", tool.Installer); err != nil {
				return err
			}
		}
		if tool.Hint != "" {
			if _, err := fmt.Fprintf(w, "         hint: %s\n", tool.Hint); err != nil {
				return err
			}
		}
	}
	_, err := fmt.Fprintf(w, "\nsummary: %d present, %d installed, %d dry-run, %d manual, %d failed\n",
		report.Summary.Present,
		report.Summary.Installed,
		report.Summary.DryRun,
		report.Summary.Manual,
		report.Summary.Failed,
	)
	return err
}

func (r *Report) add(result ToolResult) {
	r.Tools = append(r.Tools, result)
	switch result.Status {
	case StatusPresent:
		r.Summary.Present++
	case StatusInstalled:
		r.Summary.Installed++
	case StatusDryRun:
		r.Summary.DryRun++
	case StatusManual:
		r.Summary.Manual++
	case StatusFailed:
		r.Summary.Failed++
	}
}

func (o Options) withDefaults() Options {
	if o.Stdout == nil {
		o.Stdout = io.Discard
	}
	if o.Stderr == nil {
		o.Stderr = io.Discard
	}
	if o.OS == "" {
		o.OS = runtime.GOOS
	}
	if o.LookupPath == nil {
		o.LookupPath = exec.LookPath
	}
	if o.RunCommand == nil {
		o.RunCommand = runCommand
	}
	return o
}

func ensureTool(ctx context.Context, opts Options, tool Tool) ToolResult {
	before := checkTool(ctx, opts, tool)
	if before.Status == StatusPresent {
		return before
	}
	if before.Status == StatusFailed {
		return before
	}
	plan, ok := installPlan(opts, tool.Name)
	if !ok {
		before.Status = StatusManual
		before.Hint = manualHint(tool)
		return before
	}
	if opts.DryRun {
		return ToolResult{
			Name:      tool.Name,
			Status:    StatusDryRun,
			Installer: plan.Description,
			Commands:  plan.Commands,
			Detail:    "missing; would install",
			Hint:      tool.Hint,
		}
	}
	if _, err := fmt.Fprintf(opts.Stderr, "installing %s with %s\n", tool.Name, plan.Description); err != nil {
		return ToolResult{Name: tool.Name, Status: StatusFailed, Detail: err.Error(), Hint: tool.Hint}
	}
	for _, command := range plan.Commands {
		if err := opts.RunCommand(ctx, command, opts.Stdout, opts.Stderr); err != nil {
			return ToolResult{
				Name:      tool.Name,
				Status:    StatusFailed,
				Installer: plan.Description,
				Commands:  plan.Commands,
				Detail:    err.Error(),
				Hint:      tool.Hint,
			}
		}
	}
	after := checkTool(ctx, opts, tool)
	if after.Status == StatusPresent {
		after.Status = StatusInstalled
		after.Installer = plan.Description
		after.Commands = plan.Commands
		return after
	}
	after.Status = StatusFailed
	after.Installer = plan.Description
	after.Commands = plan.Commands
	if after.Detail == "not found on PATH" {
		after.Detail = "installer completed but the command is still not available"
	}
	if tool.Name == "process-compose" {
		after.Hint = "Add $(go env GOPATH)/bin to PATH, or install process-compose manually."
	}
	return after
}

func checkTool(ctx context.Context, opts Options, tool Tool) ToolResult {
	path, err := opts.LookupPath(tool.Name)
	if err != nil && tool.Name == "process-compose" {
		if goBinPath, goBinErr := goBinProcessCompose(ctx, opts); goBinErr == nil {
			path = goBinPath
			err = nil
		}
	}
	if err != nil {
		return ToolResult{Name: tool.Name, Status: StatusManual, Detail: "not found on PATH", Hint: tool.Hint}
	}
	version, err := commandVersion(ctx, opts, path, tool.VersionArgs)
	if err != nil {
		return ToolResult{Name: tool.Name, Status: StatusFailed, Path: path, Detail: fmt.Sprintf("version check failed: %v", err), Hint: tool.Hint}
	}
	return ToolResult{Name: tool.Name, Status: StatusPresent, Path: path, Version: version}
}

func commandVersion(ctx context.Context, opts Options, path string, args []string) (string, error) {
	childCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if err := opts.RunCommand(childCtx, Command{Name: path, Args: args}, &stdout, &stderr); err != nil {
		if childCtx.Err() != nil {
			return "", childCtx.Err()
		}
		if text := strings.TrimSpace(stderr.String()); text != "" {
			return "", fmt.Errorf("%w: %s", err, text)
		}
		return "", err
	}
	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	if len(lines) == 0 {
		return path, nil
	}
	if line := strings.TrimSpace(lines[0]); line != "" {
		return line, nil
	}
	return path, nil
}

func runCommand(ctx context.Context, command Command, stdout io.Writer, stderr io.Writer) error {
	cmd := exec.CommandContext(ctx, command.Name, command.Args...)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s %s: %w", command.Name, strings.Join(command.Args, " "), err)
	}
	return nil
}

type plan struct {
	Description string
	Commands    []Command
}

func installPlan(opts Options, name string) (plan, bool) {
	switch name {
	case "git":
		return systemPackagePlan(opts, packageNames{brew: []string{"git"}, apt: []string{"git"}, dnf: []string{"git"}, yum: []string{"git"}, pacman: []string{"git"}, apk: []string{"git"}}, false)
	case "go":
		return systemPackagePlan(opts, packageNames{brew: []string{"go"}, apt: []string{"golang-go"}, dnf: []string{"golang"}, yum: []string{"golang"}, pacman: []string{"go"}, apk: []string{"go"}}, false)
	case "uv":
		return uvPlan(opts)
	case "node":
		return systemPackagePlan(opts, packageNames{brew: []string{"node"}, apt: []string{"nodejs", "npm"}, dnf: []string{"nodejs", "npm"}, yum: []string{"nodejs", "npm"}, pacman: []string{"nodejs", "npm"}, apk: []string{"nodejs", "npm"}}, false)
	case "pnpm":
		return pnpmPlan(opts)
	case "npx":
		return npxPlan(opts)
	case "docker":
		return systemPackagePlan(opts, packageNames{brew: []string{"docker"}, apt: []string{"docker.io", "docker-compose-plugin"}, dnf: []string{"docker", "docker-compose-plugin"}, yum: []string{"docker", "docker-compose-plugin"}, pacman: []string{"docker", "docker-compose"}, apk: []string{"docker", "docker-cli-compose"}}, true)
	case "process-compose":
		if _, err := opts.LookupPath("go"); err == nil {
			return plan{Description: "go install", Commands: []Command{{Name: "go", Args: []string{"install", ProcessComposeInstallPackage}}}}, true
		}
		return plan{}, false
	default:
		return plan{}, false
	}
}

type packageNames struct {
	brew   []string
	apt    []string
	dnf    []string
	yum    []string
	pacman []string
	apk    []string
}

func systemPackagePlan(opts Options, names packageNames, cask bool) (plan, bool) {
	if _, err := opts.LookupPath("brew"); err == nil {
		args := []string{"install"}
		if cask && opts.OS == "darwin" {
			args = append(args, "--cask")
		}
		args = append(args, names.brew...)
		return plan{Description: "Homebrew", Commands: []Command{{Name: "brew", Args: args}}}, true
	}
	if opts.OS != "linux" {
		return plan{}, false
	}
	if len(names.apt) > 0 {
		if _, err := opts.LookupPath("apt-get"); err == nil {
			return plan{Description: "apt-get", Commands: []Command{withSudo(opts, "apt-get", "update"), withSudo(opts, "apt-get", append([]string{"install", "-y"}, names.apt...)...)}}, true
		}
	}
	if len(names.dnf) > 0 {
		if _, err := opts.LookupPath("dnf"); err == nil {
			return plan{Description: "dnf", Commands: []Command{withSudo(opts, "dnf", append([]string{"install", "-y"}, names.dnf...)...)}}, true
		}
	}
	if len(names.yum) > 0 {
		if _, err := opts.LookupPath("yum"); err == nil {
			return plan{Description: "yum", Commands: []Command{withSudo(opts, "yum", append([]string{"install", "-y"}, names.yum...)...)}}, true
		}
	}
	if len(names.pacman) > 0 {
		if _, err := opts.LookupPath("pacman"); err == nil {
			return plan{Description: "pacman", Commands: []Command{withSudo(opts, "pacman", append([]string{"-S", "--noconfirm"}, names.pacman...)...)}}, true
		}
	}
	if len(names.apk) > 0 {
		if _, err := opts.LookupPath("apk"); err == nil {
			return plan{Description: "apk", Commands: []Command{withSudo(opts, "apk", append([]string{"add"}, names.apk...)...)}}, true
		}
	}
	return plan{}, false
}

func uvPlan(opts Options) (plan, bool) {
	if p, ok := systemPackagePlan(opts, packageNames{brew: []string{"uv"}}, false); ok {
		return p, true
	}
	if _, err := opts.LookupPath("curl"); err != nil {
		return plan{}, false
	}
	if _, err := opts.LookupPath("sh"); err != nil {
		return plan{}, false
	}
	return plan{
		Description: "official uv installer",
		Commands: []Command{{
			Name: "sh",
			Args: []string{"-c", "curl -LsSf https://astral.sh/uv/install.sh | sh"},
		}},
	}, true
}

func pnpmPlan(opts Options) (plan, bool) {
	if p, ok := systemPackagePlan(opts, packageNames{brew: []string{"pnpm"}}, false); ok {
		return p, true
	}
	if _, err := opts.LookupPath("corepack"); err == nil {
		return plan{Description: "corepack", Commands: []Command{
			{Name: "corepack", Args: []string{"enable", "pnpm"}},
			{Name: "corepack", Args: []string{"prepare", "pnpm@latest", "--activate"}},
		}}, true
	}
	if _, err := opts.LookupPath("npm"); err == nil {
		return plan{Description: "npm", Commands: []Command{{Name: "npm", Args: []string{"install", "-g", "pnpm"}}}}, true
	}
	return plan{}, false
}

func npxPlan(opts Options) (plan, bool) {
	if p, ok := systemPackagePlan(opts, packageNames{brew: []string{"node"}, apt: []string{"npm"}, dnf: []string{"npm"}, yum: []string{"npm"}, pacman: []string{"npm"}, apk: []string{"npm"}}, false); ok {
		return p, true
	}
	if _, err := opts.LookupPath("npm"); err == nil {
		return plan{Description: "npm", Commands: []Command{{Name: "npm", Args: []string{"install", "-g", "npx"}}}}, true
	}
	return plan{}, false
}

func withSudo(opts Options, name string, args ...string) Command {
	if os.Geteuid() == 0 {
		return Command{Name: name, Args: append([]string(nil), args...)}
	}
	if _, err := opts.LookupPath("sudo"); err != nil {
		return Command{Name: name, Args: append([]string(nil), args...)}
	}
	return Command{Name: "sudo", Args: append([]string{name}, args...)}
}

func manualHint(tool Tool) string {
	switch tool.Name {
	case "docker":
		return "Install Docker Desktop or Docker Engine, start the daemon, then rerun `angee bootstrap`."
	case "uv":
		return "Install uv from https://docs.astral.sh/uv/getting-started/installation/."
	case "pnpm":
		return "Install pnpm from https://pnpm.io/installation, or install Node.js with corepack."
	case "process-compose":
		return "Install Go, then run `go install " + ProcessComposeInstallPackage + "`."
	default:
		return tool.Hint
	}
}

func goBinProcessCompose(ctx context.Context, opts Options) (string, error) {
	goBin, err := goBinPath(ctx, opts)
	if err != nil {
		return "", err
	}
	path := filepath.Join(goBin, "process-compose")
	if _, err := os.Stat(path); err != nil {
		return "", err
	}
	return path, nil
}

func goBinPath(ctx context.Context, opts Options) (string, error) {
	goPath, err := goEnv(ctx, opts, "GOPATH")
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(goPath) == "" {
		return "", errors.New("GOPATH is empty")
	}
	return filepath.Join(strings.TrimSpace(goPath), "bin"), nil
}

func goEnv(ctx context.Context, opts Options, key string) (string, error) {
	path, err := opts.LookupPath("go")
	if err != nil {
		return "", err
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if err := opts.RunCommand(ctx, Command{Name: path, Args: []string{"env", key}}, &stdout, &stderr); err != nil {
		if text := strings.TrimSpace(stderr.String()); text != "" {
			return "", fmt.Errorf("%w: %s", err, text)
		}
		return "", err
	}
	return strings.TrimSpace(stdout.String()), nil
}
