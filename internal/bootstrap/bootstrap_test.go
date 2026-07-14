// Tests for bootstrap generation. The script IS the product — every
// clause of the lifecycle contract (private tempdir, trap cleanup, no
// exec, decoder ladder, reserved exit codes) is pinned here, plus a
// payload round-trip proving the embedded base64 is the real bundle.
package bootstrap

import (
	"encoding/base64"
	"regexp"
	"strings"
	"testing"

	"github.com/JaydenCJ/dotduffel/internal/manifest"
)

var payload = []byte("\x1f\x8b\x08\x00fake-tgz-for-tests")

func opts() Options {
	return Options{Payload: payload, Shell: "bash"}
}

func TestScriptEmbedsPayloadRoundTrip(t *testing.T) {
	s := Script(opts())
	m := regexp.MustCompile(`duffel_b64='([^']*)'`).FindStringSubmatch(s)
	if m == nil {
		t.Fatal("no payload assignment in script")
	}
	got, err := base64.StdEncoding.DecodeString(m[1])
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(payload) {
		t.Fatal("payload does not round-trip through the script")
	}
}

func TestScriptTempdirLifecycle(t *testing.T) {
	s := Script(opts())
	if !strings.Contains(s, `mktemp -d "${TMPDIR:-/tmp}/duffel.XXXXXXXX"`) {
		t.Fatal("script does not mktemp -d a private dir")
	}
	if !strings.Contains(s, `trap 'rm -rf "$duffel_dir"' EXIT HUP INT TERM`) {
		t.Fatal("cleanup trap missing — the host would be left dirty")
	}
}

func TestScriptNeverExecsTheShell(t *testing.T) {
	// exec would replace the wrapper and orphan the EXIT trap; the
	// tempdir would survive the session. This is the bug that must
	// never come back. The shell's status still has to reach the caller,
	// so the wrapper waits and re-exits with it instead.
	for _, o := range []Options{
		{Payload: payload, Shell: "bash"},
		{Payload: payload, Shell: "sh"},
		{Payload: payload, Shell: "bash", Command: "ls"},
	} {
		s := Script(o)
		for _, line := range strings.Split(s, "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "exec ") {
				t.Fatalf("script execs the shell: %q", line)
			}
		}
		if !strings.Contains(s, "duffel_status=$?") || !strings.Contains(s, "exit $duffel_status") {
			t.Fatal("shell exit status is not passed through")
		}
	}
}

func TestScriptInteractiveShellLines(t *testing.T) {
	if s := Script(opts()); !strings.Contains(s, `bash --rcfile "$duffel_dir/.duffelrc" -i`) {
		t.Fatal("interactive bash line missing")
	}
	// POSIX sh has no --rcfile; ENV is the standard interactive hook.
	if s := Script(Options{Payload: payload, Shell: "sh"}); !strings.Contains(s, `ENV="$duffel_dir/.duffelrc" sh -i`) {
		t.Fatal("POSIX sh must use the ENV interactive hook")
	}
}

func TestScriptCommandModeEnablesAliases(t *testing.T) {
	// Aliases must work for one-off commands too: the rc wrapper and
	// the command sit on separate parsed lines, with expand_aliases on.
	s := Script(Options{Payload: payload, Shell: "bash", Command: "ll"})
	if !strings.Contains(s, "bash -O expand_aliases -c ") {
		t.Fatal("command mode does not enable alias expansion")
	}
	if !strings.Contains(s, ".duffelrc\"\nll") {
		t.Fatal("command is not on its own parsed line after the rc wrapper")
	}
}

func TestScriptCommandModeQuotesHostileCommands(t *testing.T) {
	s := Script(Options{Payload: payload, Shell: "bash", Command: `echo "it's done" && rm -- 'x y'`})
	// The full command must appear inside one properly escaped word.
	if !strings.Contains(s, `it'\''s done`) {
		t.Fatal("embedded single quote not escaped")
	}
}

func TestScriptDecoderLadder(t *testing.T) {
	// GNU/busybox, then BSD, then openssl: the duffel must land on
	// alpine, debian, and macOS hosts alike.
	s := Script(opts())
	for _, want := range []string{"base64 -d", "base64 -D", "openssl base64 -d -A"} {
		if !strings.Contains(s, want) {
			t.Errorf("decoder ladder missing %q", want)
		}
	}
	// The multi-KiB base64 blob must not linger in the shell
	// environment for the whole session.
	if !strings.Contains(s, "unset duffel_b64") {
		t.Error("payload variable is never unset")
	}
}

func TestScriptReservedExitCodes(t *testing.T) {
	s := Script(opts())
	for code, marker := range map[int]string{
		ExitNoTempdir:     "exit 97",
		ExitNoDecoder:     "exit 96",
		ExitExtractFailed: "exit 95",
	} {
		if !strings.Contains(s, marker) {
			t.Errorf("exit code %d (%s) not wired into the script", code, marker)
		}
	}
}

func TestScriptIsDeterministic(t *testing.T) {
	if Script(opts()) != Script(opts()) {
		t.Fatal("same options produced different scripts")
	}
}

func TestRCFileLoadsHostDefaultsFirst(t *testing.T) {
	rc := RCFile(manifest.Manifest{Shell: "bash", Entry: "duffelrc"})
	hostIdx := strings.Index(rc, `. "$HOME/.bashrc"`)
	entryIdx := strings.Index(rc, `. "$DUFFEL_DIR/duffelrc"`)
	if hostIdx < 0 || entryIdx < 0 {
		t.Fatalf("rc wrapper incomplete:\n%s", rc)
	}
	if hostIdx > entryIdx {
		t.Fatal("entry sourced before host defaults — the duffel must augment, not replace")
	}
	if !strings.Contains(rc, "/etc/bash.bashrc") {
		t.Fatal("system bashrc not sourced")
	}
}

func TestRCFileShVariantAvoidsBashisms(t *testing.T) {
	rc := RCFile(manifest.Manifest{Shell: "sh", Entry: "duffelrc"})
	if strings.Contains(rc, "bashrc") {
		t.Fatal("sh wrapper must not source bash rc files")
	}
	if !strings.Contains(rc, `"$HOME/.shrc"`) {
		t.Fatal("sh wrapper should honor ~/.shrc")
	}
}

func TestRCFileExportsSortedQuotedEnv(t *testing.T) {
	rc := RCFile(manifest.Manifest{Shell: "bash", Entry: "duffelrc", Env: map[string]string{
		"ZED":    "z",
		"EDITOR": "vim -u NONE",
	}})
	editorIdx := strings.Index(rc, `export EDITOR='vim -u NONE'`)
	zedIdx := strings.Index(rc, "export ZED=z")
	if editorIdx < 0 || zedIdx < 0 {
		t.Fatalf("env exports missing or badly quoted:\n%s", rc)
	}
	if editorIdx > zedIdx {
		t.Fatal("env exports not sorted — output would churn between packs")
	}
}

func TestRCFileBinPathAndFinalStatus(t *testing.T) {
	rc := RCFile(manifest.Manifest{Shell: "bash", Entry: "duffelrc"})
	if !strings.Contains(rc, `PATH="$DUFFEL_DIR/bin:$PATH"`) {
		t.Fatal("bundled bin/ never joins PATH")
	}
	// The last [ -f ... ] && ... guard fails when optional files are
	// absent; the rc file must not hand that status to an interactive
	// shell (it would land in the session's initial $?).
	if !strings.HasSuffix(rc, "true\n") {
		t.Fatalf("rc file ends with %q", rc[len(rc)-10:])
	}
}

func TestShellQuoteTable(t *testing.T) {
	cases := map[string]string{
		"":            "''",
		"plain":       "plain",
		"a/b.c-d_e":   "a/b.c-d_e",
		"two words":   "'two words'",
		"it's":        `'it'\''s'`,
		"$HOME":       `'$HOME'`,
		"a;rm -rf /":  `'a;rm -rf /'`,
		"line\nbreak": "'line\nbreak'",
		"back`tick`":  "'back`tick`'",
	}
	for in, want := range cases {
		if got := ShellQuote(in); got != want {
			t.Errorf("ShellQuote(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestQuoteArgvIsCopyPasteable(t *testing.T) {
	got := QuoteArgv([]string{"ssh", "-t", "devbox", "sh -c 'echo hi'"})
	want := `ssh -t devbox 'sh -c '\''echo hi'\'''`
	if got != want {
		t.Fatalf("QuoteArgv = %s", got)
	}
}
