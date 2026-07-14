// Integration tests for the CLI, driven in-process through Run with
// injected streams. The transport tests use --print to assert the exact
// argv; the local-shell tests execute the real bootstrap under /bin/sh,
// which is the closest thing to an ssh session that stays offline and
// deterministic.
package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// run drives the CLI in-process and captures both streams.
func run(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := Run(args, strings.NewReader(""), &out, &errb)
	return code, out.String(), errb.String()
}

// duffelDir builds a config directory with a manifest and dotfiles;
// extra maps of filename→content are merged over the defaults.
func duffelDir(t *testing.T, manifestJSON string, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	if manifestJSON == "" {
		manifestJSON = `{
  "files": [ { "from": "duffelrc" }, { "from": "aliases.sh" } ],
  "env": { "DUFFEL": "1" }
}`
	}
	defaults := map[string]string{
		"duffel.json": manifestJSON,
		"duffelrc":    "[ -f \"$DUFFEL_DIR/aliases.sh\" ] && . \"$DUFFEL_DIR/aliases.sh\"\nDUFFEL_MARK=loaded\n",
		"aliases.sh":  "alias dfl='echo duffel-alias-ran'\n",
	}
	for name, content := range files {
		defaults[name] = content
	}
	for name, content := range defaults {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func manifestArg(dir string) string {
	return "--manifest=" + filepath.Join(dir, "duffel.json")
}

func TestVersionOutput(t *testing.T) {
	code, out, _ := run(t, "--version")
	if code != 0 || out != "dotduffel 0.1.0\n" {
		t.Fatalf("code=%d out=%q", code, out)
	}
	code, out2, _ := run(t, "version")
	if code != 0 || out2 != out {
		t.Fatalf("version subcommand differs: %q", out2)
	}
}

func TestHelpListsEveryCommand(t *testing.T) {
	code, out, _ := run(t, "--help")
	if code != 0 {
		t.Fatalf("code=%d", code)
	}
	for _, cmd := range []string{"init", "ls", "pack", "emit", "ssh", "docker", "podman", "exec", "sh"} {
		if !strings.Contains(out, "\n  "+cmd) {
			t.Errorf("help missing command %q", cmd)
		}
	}
}

func TestUnknownCommandAndNoArgsFailWithUsage(t *testing.T) {
	code, _, errs := run(t, "teleport")
	if code != 2 || !strings.Contains(errs, `unknown command "teleport"`) {
		t.Fatalf("code=%d errs=%q", code, errs)
	}
	code, out, errs := run(t)
	if code != 2 || out != "" || !strings.Contains(errs, "Usage:") {
		t.Fatalf("code=%d out=%q", code, out)
	}
}

func TestManifestLookupOrder(t *testing.T) {
	// No manifest anywhere: the error explains the search path and the
	// way out.
	t.Setenv("HOME", t.TempDir())
	t.Setenv("DOTDUFFEL_MANIFEST", "")
	code, _, errs := run(t, "ls")
	if code != 2 || !strings.Contains(errs, "dotduffel init") {
		t.Fatalf("code=%d errs=%q", code, errs)
	}
	// $DOTDUFFEL_MANIFEST is honored.
	dir := duffelDir(t, "", nil)
	t.Setenv("DOTDUFFEL_MANIFEST", filepath.Join(dir, "duffel.json"))
	code, out, errs := run(t, "ls")
	if code != 0 || !strings.Contains(out, "duffelrc") {
		t.Fatalf("env lookup: code=%d out=%q errs=%q", code, out, errs)
	}
	// ~/.config/dotduffel/duffel.json is the final fallback.
	t.Setenv("DOTDUFFEL_MANIFEST", "")
	home := t.TempDir()
	t.Setenv("HOME", home)
	cfg := filepath.Join(home, ".config", "dotduffel")
	os.MkdirAll(cfg, 0o755)
	os.WriteFile(filepath.Join(cfg, "duffel.json"), []byte(`{"files":[{"from":"duffelrc"}]}`), 0o644)
	os.WriteFile(filepath.Join(cfg, "duffelrc"), []byte("# rc\n"), 0o644)
	code, out, errs = run(t, "ls")
	if code != 0 || !strings.Contains(out, "duffelrc") {
		t.Fatalf("home lookup: code=%d out=%q errs=%q", code, out, errs)
	}
}

func TestInitWritesWorkingStarter(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cfg")
	code, out, errs := run(t, "init", "--dir", dir)
	if code != 0 {
		t.Fatalf("code=%d errs=%q", code, errs)
	}
	for _, name := range []string{"duffel.json", "duffelrc", "aliases.sh"} {
		if !strings.Contains(out, name) {
			t.Errorf("init output missing %q", name)
		}
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("starter %s not created: %v", name, err)
		}
	}
	// The starter must be immediately usable, not just present.
	code, _, errs = run(t, "--manifest", filepath.Join(dir, "duffel.json"), "emit")
	if code != 0 {
		t.Fatalf("starter manifest does not emit: %q", errs)
	}
	// Without --dir, output abbreviates $HOME as "~" so the printed paths
	// read the same for every user (this is the README's captured output).
	home := t.TempDir()
	t.Setenv("HOME", home)
	code, out, errs = run(t, "init")
	if code != 0 {
		t.Fatalf("init in home: code=%d errs=%q", code, errs)
	}
	if !strings.Contains(out, "created ~/.config/dotduffel/duffel.json") {
		t.Errorf("init output not home-abbreviated: %q", out)
	}
	if strings.Contains(out, home) {
		t.Errorf("init output leaks absolute home path: %q", out)
	}
}

func TestInitRefusesToClobberWithoutForce(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cfg")
	run(t, "init", "--dir", dir)
	code, _, errs := run(t, "init", "--dir", dir)
	if code != 2 || !strings.Contains(errs, "--force") {
		t.Fatalf("code=%d errs=%q", code, errs)
	}
	code, _, _ = run(t, "init", "--dir", dir, "--force")
	if code != 0 {
		t.Fatalf("--force failed: code=%d", code)
	}
}

func TestLsShowsMembersAndBudget(t *testing.T) {
	dir := duffelDir(t, "", nil)
	code, out, errs := run(t, manifestArg(dir), "ls")
	if code != 0 {
		t.Fatalf("code=%d errs=%q", code, errs)
	}
	for _, want := range []string{"duffelrc", "aliases.sh", ".duffelrc", "bootstrap", "budget"} {
		if !strings.Contains(out, want) {
			t.Errorf("ls output missing %q:\n%s", want, out)
		}
	}
}

func TestPackIsReproducibleAcrossRuns(t *testing.T) {
	dir := duffelDir(t, "", nil)
	out1 := filepath.Join(t.TempDir(), "one.tgz")
	out2 := filepath.Join(t.TempDir(), "two.tgz")
	if code, _, errs := run(t, manifestArg(dir), "pack", "-o", out1); code != 0 {
		t.Fatalf("pack 1: %q", errs)
	}
	if code, _, errs := run(t, manifestArg(dir), "pack", "-o", out2); code != 0 {
		t.Fatalf("pack 2: %q", errs)
	}
	a, _ := os.ReadFile(out1)
	b, _ := os.ReadFile(out2)
	if !bytes.Equal(a, b) {
		t.Fatal("two packs of the same manifest differ byte-for-byte")
	}
}

func TestPackToStdoutIsRawArchive(t *testing.T) {
	dir := duffelDir(t, "", nil)
	code, out, _ := run(t, manifestArg(dir), "pack", "-o", "-")
	if code != 0 {
		t.Fatalf("code=%d", code)
	}
	if len(out) < 2 || out[0] != 0x1f || out[1] != 0x8b {
		t.Fatal("stdout is not a gzip stream")
	}
	if strings.Contains(out, "packed") {
		t.Fatal("chatter mixed into the raw archive on stdout")
	}
}

func TestEmitPrintsRunnableBootstrap(t *testing.T) {
	dir := duffelDir(t, "", nil)
	code, out, _ := run(t, manifestArg(dir), "emit")
	if code != 0 {
		t.Fatalf("code=%d", code)
	}
	for _, want := range []string{"mktemp -d", "trap 'rm -rf", "duffel_b64='", "--rcfile"} {
		if !strings.Contains(out, want) {
			t.Errorf("emit output missing %q", want)
		}
	}
}

func TestGuardRefusalIsExitOne(t *testing.T) {
	dir := duffelDir(t, `{"files":[{"from":"duffelrc"},{"from":"leak.txt"}]}`,
		map[string]string{"leak.txt": "-----BEGIN RSA PRIVATE KEY-----\nAAAA\n"})
	code, _, errs := run(t, manifestArg(dir), "emit")
	if code != 1 {
		t.Fatalf("code=%d, want 1", code)
	}
	if !strings.Contains(errs, "leak.txt") || !strings.Contains(errs, "private-key") {
		t.Fatalf("refusal does not name the file and rule: %q", errs)
	}
	if !strings.Contains(errs, "allow_secrets") {
		t.Fatalf("refusal does not mention the override: %q", errs)
	}
}

func TestAllowSecretsOverridesGuard(t *testing.T) {
	dir := duffelDir(t, `{"files":[{"from":"duffelrc"},{"from":"leak.txt","allow_secrets":true}]}`,
		map[string]string{"leak.txt": "-----BEGIN RSA PRIVATE KEY-----\nAAAA\n"})
	code, _, errs := run(t, manifestArg(dir), "emit")
	if code != 0 {
		t.Fatalf("code=%d errs=%q", code, errs)
	}
}

func TestBudgetOverflowNamesTheOffenders(t *testing.T) {
	// ~3 KiB of digits of a simple counter — low enough entropy to read,
	// high enough that a 1 KiB budget must overflow deterministically.
	var big strings.Builder
	for i := 0; i < 600; i++ {
		big.WriteString(strings.Repeat(string(rune('a'+i%26)), i%17+1))
		big.WriteByte('\n')
	}
	dir := duffelDir(t, `{"budget_kb":1,"files":[{"from":"duffelrc"},{"from":"big.conf"}]}`,
		map[string]string{"big.conf": big.String()})
	code, _, errs := run(t, manifestArg(dir), "emit")
	if code != 1 {
		t.Fatalf("code=%d, want 1", code)
	}
	if !strings.Contains(errs, "big.conf") || !strings.Contains(errs, "budget_kb") {
		t.Fatalf("overflow report unhelpful: %q", errs)
	}
}

func TestMissingEntryIsConfigError(t *testing.T) {
	dir := duffelDir(t, `{"entry":"myrc","files":[{"from":"aliases.sh"}]}`, nil)
	code, _, errs := run(t, manifestArg(dir), "emit")
	if code != 2 || !strings.Contains(errs, `"myrc"`) {
		t.Fatalf("code=%d errs=%q", code, errs)
	}
}

func TestSSHPrintShowsExactInvocation(t *testing.T) {
	dir := duffelDir(t, "", nil)
	code, out, errs := run(t, manifestArg(dir), "ssh", "--print", "devbox", "-p", "2222")
	if code != 0 {
		t.Fatalf("code=%d errs=%q", code, errs)
	}
	if !strings.HasPrefix(out, "ssh -t -p 2222 devbox ") {
		t.Fatalf("argv head wrong: %q", out[:50])
	}
	if !strings.Contains(out, "duffel_b64=") {
		t.Fatal("payload missing from printed command")
	}
	// A missing host is caught before anything is packed or spawned.
	code, _, errs = run(t, manifestArg(dir), "ssh", "--print")
	if code != 2 || !strings.Contains(errs, "usage:") {
		t.Fatalf("code=%d errs=%q", code, errs)
	}
}

func TestDockerPrintCommandModeUsesPlainExec(t *testing.T) {
	dir := duffelDir(t, "", nil)
	code, out, _ := run(t, manifestArg(dir), "docker", "--print", "--command", "ls", "mybox")
	if code != 0 {
		t.Fatalf("code=%d", code)
	}
	if !strings.HasPrefix(out, "docker exec -i mybox sh -c ") {
		t.Fatalf("argv head wrong: %q", out[:40])
	}
	if strings.Contains(out, "-it ") {
		t.Fatal("command mode must not allocate a tty")
	}
}

func TestExecPrintAppendsToTransport(t *testing.T) {
	dir := duffelDir(t, "", nil)
	code, out, _ := run(t, manifestArg(dir), "exec", "--print", "kubectl", "exec", "-i", "mypod", "--")
	if code != 0 {
		t.Fatalf("code=%d", code)
	}
	if !strings.HasPrefix(out, "kubectl exec -i mypod -- sh -c ") {
		t.Fatalf("argv head wrong: %q", out[:45])
	}
}

func TestShRunsBootstrapAndCleansUp(t *testing.T) {
	// The real thing: extract, load env + entry + aliases, run a
	// command, and leave nothing behind. This is the ssh session minus
	// the network.
	dir := duffelDir(t, "", nil)
	code, out, errs := run(t, manifestArg(dir), "sh", "--command",
		`echo "dir=$DUFFEL_DIR"; echo "env=$DUFFEL"; echo "mark=$DUFFEL_MARK"; dfl`)
	if code != 0 {
		t.Fatalf("code=%d errs=%q", code, errs)
	}
	for _, want := range []string{"env=1", "mark=loaded", "duffel-alias-ran"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	// The tempdir named in the output must be gone.
	var tempdir string
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "dir=") {
			tempdir = strings.TrimPrefix(line, "dir=")
		}
	}
	if tempdir == "" {
		t.Fatal("bootstrap did not report DUFFEL_DIR")
	}
	if _, err := os.Stat(tempdir); !os.IsNotExist(err) {
		t.Fatalf("tempdir %s survived the session", tempdir)
	}
}

func TestShExitCodeAndHostileQuoting(t *testing.T) {
	dir := duffelDir(t, "", nil)
	code, _, _ := run(t, manifestArg(dir), "sh", "--command", "exit 42")
	if code != 42 {
		t.Fatalf("code=%d, want 42", code)
	}
	code, out, errs := run(t, manifestArg(dir), "sh", "--command", `printf '%s\n' "it's \$fine"`)
	if code != 0 {
		t.Fatalf("code=%d errs=%q", code, errs)
	}
	if !strings.Contains(out, "it's $fine") {
		t.Fatalf("quoting mangled: %q", out)
	}
}

func TestShellSelectionSh(t *testing.T) {
	// shell:"sh" must run the whole lifecycle under plain sh.
	dir := duffelDir(t, `{"shell":"sh","files":[{"from":"duffelrc"},{"from":"aliases.sh"}]}`, nil)
	code, out, errs := run(t, manifestArg(dir), "sh", "--command", "echo mark=$DUFFEL_MARK")
	if code != 0 {
		t.Fatalf("code=%d errs=%q", code, errs)
	}
	if !strings.Contains(out, "mark=loaded") {
		t.Fatalf("sh entry not sourced: %q", out)
	}
}

func TestBundledBinJoinsPath(t *testing.T) {
	dir := duffelDir(t,
		`{"files":[{"from":"duffelrc"},{"from":"tools/*.sh","to":"bin/"}]}`,
		map[string]string{"tools/hello.sh": "#!/bin/sh\necho hello-from-duffel-bin\n"})
	// Mark the tool executable so the bundle preserves the bit.
	os.Chmod(filepath.Join(dir, "tools", "hello.sh"), 0o755)
	code, out, errs := run(t, manifestArg(dir), "sh", "--command", "hello.sh")
	if code != 0 {
		t.Fatalf("code=%d errs=%q", code, errs)
	}
	if !strings.Contains(out, "hello-from-duffel-bin") {
		t.Fatalf("bundled bin not on PATH: %q", out)
	}
}
