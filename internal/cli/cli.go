// Package cli implements the dotduffel command-line interface.
//
// The entry point is Run, which takes argv and explicit streams and
// returns a process exit code. Keeping the CLI a pure function of its
// inputs (no os.Exit, no global state) is what lets the integration
// tests drive every subcommand in-process, deterministically, with no
// PATH or working-directory coupling. Only the transport commands
// (ssh/docker/podman/exec/sh) spawn a child process — and each of them
// has a --print mode that shows the exact argv instead.
//
// Exit codes:
//
//	0  success (transports pass the child's exit code through)
//	1  refused to pack — the secret guard tripped or the bundle blew
//	   the argv budget
//	2  usage, configuration or I/O error
package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/JaydenCJ/dotduffel/internal/bootstrap"
	"github.com/JaydenCJ/dotduffel/internal/bundle"
	"github.com/JaydenCJ/dotduffel/internal/guard"
	"github.com/JaydenCJ/dotduffel/internal/manifest"
	"github.com/JaydenCJ/dotduffel/internal/remote"
	"github.com/JaydenCJ/dotduffel/internal/version"
)

const (
	exitOK      = 0
	exitRefused = 1
	exitError   = 2
)

const usageText = `dotduffel %s — pack a dotfiles duffel, inject it anywhere, leave no trace

Usage:
  dotduffel [--manifest FILE] <command> [flags] [args]

Commands:
  init [--dir DIR] [--force]
        write a starter manifest, entry file and aliases
  ls    show what the manifest resolves to, with sizes and budget use
  pack [-o FILE]
        write the raw bundle (deterministic tar.gz); -o - for stdout
  emit [--command CMD]
        print the self-extracting bootstrap script
  ssh [--print] [--command CMD] <host> [ssh-args...]
        open an ssh session on <host> with the duffel injected
  docker [--print] [--command CMD] <container>
        docker exec into <container> with the duffel injected
  podman [--print] [--command CMD] <container>
        podman exec into <container> with the duffel injected
  exec [--print] [--command CMD] <transport argv...>
        append the duffel to any transport, e.g. kubectl exec -it pod --
  sh [--print] [--command CMD]
        run the duffel in a local shell (test drive, same lifecycle)
  version
        print the version

The manifest is found via --manifest, $DOTDUFFEL_MANIFEST, ./duffel.json,
then ~/.config/dotduffel/duffel.json. Exit codes: 0 ok, 1 refused to pack
(secret guard or size budget), 2 usage/config/IO error; transport commands
pass the child's exit code through.
`

// Run executes dotduffel with explicit streams and returns the exit code.
func Run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	mpath := ""
	rest := args
	for len(rest) > 0 {
		switch {
		case rest[0] == "--manifest" && len(rest) > 1:
			mpath, rest = rest[1], rest[2:]
		case strings.HasPrefix(rest[0], "--manifest="):
			mpath, rest = strings.TrimPrefix(rest[0], "--manifest="), rest[1:]
		case rest[0] == "--version" || rest[0] == "version":
			fmt.Fprintf(stdout, "dotduffel %s\n", version.Version)
			return exitOK
		case rest[0] == "--help" || rest[0] == "-h" || rest[0] == "help":
			fmt.Fprintf(stdout, usageText, version.Version)
			return exitOK
		default:
			goto dispatch
		}
	}
dispatch:
	if len(rest) == 0 {
		fmt.Fprintf(stderr, usageText, version.Version)
		return exitError
	}
	cmd, cmdArgs := rest[0], rest[1:]
	app := &app{mpath: mpath, stdin: stdin, stdout: stdout, stderr: stderr}
	switch cmd {
	case "init":
		return app.cmdInit(cmdArgs)
	case "ls":
		return app.cmdLs(cmdArgs)
	case "pack":
		return app.cmdPack(cmdArgs)
	case "emit":
		return app.cmdEmit(cmdArgs)
	case "ssh":
		return app.cmdSSH(cmdArgs)
	case "docker", "podman":
		return app.cmdContainer(cmd, cmdArgs)
	case "exec":
		return app.cmdExec(cmdArgs)
	case "sh":
		return app.cmdSh(cmdArgs)
	default:
		fmt.Fprintf(stderr, "dotduffel: unknown command %q (see dotduffel --help)\n", cmd)
		return exitError
	}
}

type app struct {
	mpath  string
	stdin  io.Reader
	stdout io.Writer
	stderr io.Writer
}

func (a *app) flags(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(a.stderr)
	return fs
}

func (a *app) errorf(format string, args ...any) int {
	fmt.Fprintf(a.stderr, "dotduffel: "+format+"\n", args...)
	return exitError
}

// findManifest resolves the manifest path from flag, environment,
// working directory, then the XDG default — first hit wins.
func (a *app) findManifest() (string, error) {
	if a.mpath != "" {
		return a.mpath, nil
	}
	if p := os.Getenv("DOTDUFFEL_MANIFEST"); p != "" {
		return p, nil
	}
	if _, err := os.Stat("duffel.json"); err == nil {
		return "duffel.json", nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("no manifest found and home directory unknown: %w", err)
	}
	p := filepath.Join(home, ".config", "dotduffel", "duffel.json")
	if _, err := os.Stat(p); err == nil {
		return p, nil
	}
	return "", fmt.Errorf("no manifest found (tried ./duffel.json and %s) — run \"dotduffel init\" to create one", p)
}

// load runs the full pack pipeline up to the bundle members: manifest →
// resolve → read → guard. It writes findings to stderr itself and
// returns a non-zero code for the caller to pass through.
func (a *app) load() (manifest.Manifest, []bundle.File, int) {
	mpath, err := a.findManifest()
	if err != nil {
		return manifest.Manifest{}, nil, a.errorf("%v", err)
	}
	m, err := manifest.Load(mpath)
	if err != nil {
		return manifest.Manifest{}, nil, a.errorf("%v", err)
	}
	home, _ := os.UserHomeDir()
	base := filepath.Dir(mpath)
	resolved, err := manifest.Resolve(m, base, home)
	if err != nil {
		return manifest.Manifest{}, nil, a.errorf("%s: %v", mpath, err)
	}
	if !manifest.HasEntry(m, resolved) {
		return manifest.Manifest{}, nil, a.errorf("%s: entry %q is not among the packed files — add { \"from\": \"...\", \"to\": %q } to files", mpath, m.Entry, m.Entry)
	}
	files, err := bundle.Load(resolved)
	if err != nil {
		return manifest.Manifest{}, nil, a.errorf("%v", err)
	}
	if findings := guard.Scan(files); len(findings) > 0 {
		noun := "findings"
		if len(findings) == 1 {
			noun = "finding"
		}
		fmt.Fprintf(a.stderr, "dotduffel: refusing to pack — %d secret-guard %s:\n", len(findings), noun)
		for _, f := range findings {
			fmt.Fprintf(a.stderr, "  %s\n", f)
		}
		fmt.Fprintf(a.stderr, "override per file with \"allow_secrets\": true in the manifest if this is intentional\n")
		return manifest.Manifest{}, nil, exitRefused
	}
	files = append(files, bundle.Synthetic(manifest.RCName, []byte(bootstrap.RCFile(m))))
	sort.Slice(files, func(i, j int) bool { return files[i].Dest < files[j].Dest })
	return m, files, exitOK
}

// script builds the bootstrap and enforces the argv budget, explaining
// overflows in terms of the largest members.
func (a *app) script(command string) (string, int) {
	m, files, code := a.load()
	if code != exitOK {
		return "", code
	}
	tgz, err := bundle.Build(files)
	if err != nil {
		return "", a.errorf("%v", err)
	}
	s := bootstrap.Script(bootstrap.Options{Payload: tgz, Shell: m.Shell, Command: command})
	budget := m.BudgetKB * 1024
	if len(s) > budget {
		fmt.Fprintf(a.stderr, "dotduffel: refusing to pack — bootstrap is %s, budget is %s (budget_kb=%d)\n",
			human(len(s)), human(budget), m.BudgetKB)
		fmt.Fprintf(a.stderr, "largest members:\n")
		for _, f := range bundle.Largest(files, 5) {
			fmt.Fprintf(a.stderr, "  %8s  %s\n", human(len(f.Data)), f.Dest)
		}
		fmt.Fprintf(a.stderr, "trim the manifest or raise budget_kb (max %d)\n", manifest.MaxBudgetKB)
		return "", exitRefused
	}
	return s, exitOK
}

func (a *app) cmdLs(args []string) int {
	fs := a.flags("ls")
	if err := fs.Parse(args); err != nil {
		return exitError
	}
	m, files, code := a.load()
	if code != exitOK {
		return code
	}
	tgz, err := bundle.Build(files)
	if err != nil {
		return a.errorf("%v", err)
	}
	s := bootstrap.Script(bootstrap.Options{Payload: tgz, Shell: m.Shell})
	tw := tabwriter.NewWriter(a.stdout, 2, 8, 2, ' ', 0)
	fmt.Fprintf(tw, "MODE\tSIZE\tDEST\n")
	for _, f := range files {
		fmt.Fprintf(tw, "%o\t%s\t%s\n", f.Mode, human(len(f.Data)), f.Dest)
	}
	tw.Flush()
	budget := m.BudgetKB * 1024
	fmt.Fprintf(a.stdout, "%d files, %s raw -> %s packed -> %s bootstrap (%d%% of %s budget)\n",
		len(files), human(bundle.RawSize(files)), human(len(tgz)), human(len(s)),
		len(s)*100/budget, human(budget))
	return exitOK
}

func (a *app) cmdPack(args []string) int {
	fs := a.flags("pack")
	out := fs.String("o", "duffel.tgz", "output file, or - for stdout")
	if err := fs.Parse(args); err != nil {
		return exitError
	}
	_, files, code := a.load()
	if code != exitOK {
		return code
	}
	tgz, err := bundle.Build(files)
	if err != nil {
		return a.errorf("%v", err)
	}
	if *out == "-" {
		if _, err := a.stdout.Write(tgz); err != nil {
			return a.errorf("%v", err)
		}
		return exitOK
	}
	if err := os.WriteFile(*out, tgz, 0o644); err != nil {
		return a.errorf("%v", err)
	}
	fmt.Fprintf(a.stdout, "packed %d files -> %s (%s, reproducible)\n", len(files), *out, human(len(tgz)))
	return exitOK
}

func (a *app) cmdEmit(args []string) int {
	fs := a.flags("emit")
	command := fs.String("command", "", "run CMD instead of an interactive shell")
	if err := fs.Parse(args); err != nil {
		return exitError
	}
	s, code := a.script(*command)
	if code != exitOK {
		return code
	}
	fmt.Fprint(a.stdout, s)
	return exitOK
}

func (a *app) cmdSSH(args []string) int {
	fs := a.flags("ssh")
	printOnly := fs.Bool("print", false, "print the ssh command instead of running it")
	command := fs.String("command", "", "run CMD on the host instead of an interactive shell")
	if err := fs.Parse(args); err != nil {
		return exitError
	}
	if fs.NArg() < 1 {
		return a.errorf("usage: dotduffel ssh [--print] [--command CMD] <host> [ssh-args...]")
	}
	host, sshArgs := fs.Arg(0), fs.Args()[1:]
	s, code := a.script(*command)
	if code != exitOK {
		return code
	}
	argv, err := remote.SSH(host, sshArgs, s, *command == "")
	if err != nil {
		return a.errorf("%v", err)
	}
	return a.dispatch(argv, *printOnly)
}

func (a *app) cmdContainer(tool string, args []string) int {
	fs := a.flags(tool)
	printOnly := fs.Bool("print", false, "print the "+tool+" command instead of running it")
	command := fs.String("command", "", "run CMD in the container instead of an interactive shell")
	if err := fs.Parse(args); err != nil {
		return exitError
	}
	if fs.NArg() != 1 {
		return a.errorf("usage: dotduffel %s [--print] [--command CMD] <container>", tool)
	}
	s, code := a.script(*command)
	if code != exitOK {
		return code
	}
	argv, err := remote.Container(tool, fs.Arg(0), s, *command == "")
	if err != nil {
		return a.errorf("%v", err)
	}
	return a.dispatch(argv, *printOnly)
}

func (a *app) cmdExec(args []string) int {
	fs := a.flags("exec")
	printOnly := fs.Bool("print", false, "print the command instead of running it")
	command := fs.String("command", "", "run CMD on the target instead of an interactive shell")
	if err := fs.Parse(args); err != nil {
		return exitError
	}
	if fs.NArg() < 1 {
		return a.errorf("usage: dotduffel exec [--print] [--command CMD] <transport argv...>")
	}
	s, code := a.script(*command)
	if code != exitOK {
		return code
	}
	argv, err := remote.Exec(fs.Args(), s)
	if err != nil {
		return a.errorf("%v", err)
	}
	return a.dispatch(argv, *printOnly)
}

func (a *app) cmdSh(args []string) int {
	fs := a.flags("sh")
	printOnly := fs.Bool("print", false, "print the command instead of running it")
	command := fs.String("command", "", "run CMD instead of an interactive shell")
	if err := fs.Parse(args); err != nil {
		return exitError
	}
	if fs.NArg() != 0 {
		return a.errorf("usage: dotduffel sh [--print] [--command CMD]")
	}
	s, code := a.script(*command)
	if code != exitOK {
		return code
	}
	return a.dispatch(remote.Local(s), *printOnly)
}

// dispatch either prints the argv (copy-pasteable, shell-quoted) or
// runs it wired to the real streams, passing the child's exit through.
func (a *app) dispatch(argv []string, printOnly bool) int {
	if printOnly {
		fmt.Fprintln(a.stdout, bootstrap.QuoteArgv(argv))
		return exitOK
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdin = a.stdin
	cmd.Stdout = a.stdout
	cmd.Stderr = a.stderr
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return ee.ExitCode()
		}
		return a.errorf("%s: %v", argv[0], err)
	}
	return exitOK
}

// displayPath abbreviates the user's home directory prefix as "~" so
// init output reads the same for every user regardless of where their
// home lives. Paths outside $HOME are returned unchanged.
func displayPath(p string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return p
	}
	if p == home {
		return "~"
	}
	if strings.HasPrefix(p, home+string(filepath.Separator)) {
		return "~" + p[len(home):]
	}
	return p
}

func (a *app) cmdInit(args []string) int {
	fs := a.flags("init")
	dir := fs.String("dir", "", "directory to create the starter files in (default ~/.config/dotduffel)")
	force := fs.Bool("force", false, "overwrite existing files")
	if err := fs.Parse(args); err != nil {
		return exitError
	}
	target := *dir
	if target == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return a.errorf("home directory unknown: %v (use --dir)", err)
		}
		target = filepath.Join(home, ".config", "dotduffel")
	}
	if err := os.MkdirAll(target, 0o755); err != nil {
		return a.errorf("%v", err)
	}
	starters := []struct {
		name, content string
	}{
		{"duffel.json", starterManifest},
		{"duffelrc", starterEntry},
		{"aliases.sh", starterAliases},
	}
	for _, s := range starters {
		p := filepath.Join(target, s.name)
		if !*force {
			if _, err := os.Stat(p); err == nil {
				return a.errorf("%s already exists (use --force to overwrite)", p)
			}
		}
		if err := os.WriteFile(p, []byte(s.content), 0o644); err != nil {
			return a.errorf("%v", err)
		}
		fmt.Fprintf(a.stdout, "created %s\n", displayPath(p))
	}
	fmt.Fprintf(a.stdout, "next: edit %s, then test-drive with \"dotduffel sh\"\n", displayPath(filepath.Join(target, "duffel.json")))
	return exitOK
}

const starterManifest = `{
  "entry": "duffelrc",
  "shell": "bash",
  "budget_kb": 64,
  "files": [
    { "from": "duffelrc" },
    { "from": "aliases.sh" }
  ],
  "env": { "DUFFEL": "1" }
}
`

const starterEntry = `# dotduffel entry — sourced after the host's own rc files, on every target.
[ -f "$DUFFEL_DIR/aliases.sh" ] && . "$DUFFEL_DIR/aliases.sh"

# Mark the prompt so you can tell a duffel'd shell from a bare one.
case "${PS1:-}" in
  *duffel*) ;;
  *) PS1="(duffel) ${PS1:-\$ }" ;;
esac
`

const starterAliases = `# dotduffel aliases — edit freely; this file travels with every session.
alias ll='ls -alF'
alias la='ls -A'
alias ..='cd ..'
alias gs='git status'
`

// human renders a byte count the way a person reads one.
func human(n int) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%d B", n)
	case n < 1024*1024:
		return fmt.Sprintf("%.1f KiB", float64(n)/1024)
	default:
		return fmt.Sprintf("%.1f MiB", float64(n)/(1024*1024))
	}
}
