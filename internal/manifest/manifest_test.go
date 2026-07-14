// Tests for manifest loading and resolution. Everything runs against
// t.TempDir() with an explicit fake home, so no test depends on the
// machine it runs on.
package manifest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeManifest drops a manifest file into dir and returns its path.
func writeManifest(t *testing.T, dir, content string) string {
	t.Helper()
	p := filepath.Join(dir, "duffel.json")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// touch creates a small file (with parents) and returns its path.
func touch(t *testing.T, dir, rel, content string) string {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadAppliesDefaults(t *testing.T) {
	dir := t.TempDir()
	p := writeManifest(t, dir, `{"files":[{"from":"duffelrc"}]}`)
	m, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if m.Entry != "duffelrc" {
		t.Errorf("default entry = %q, want duffelrc", m.Entry)
	}
	if m.Shell != "bash" {
		t.Errorf("default shell = %q, want bash", m.Shell)
	}
	if m.BudgetKB != DefaultBudgetKB {
		t.Errorf("default budget = %d, want %d", m.BudgetKB, DefaultBudgetKB)
	}
}

func TestLoadRejectsMalformedJSON(t *testing.T) {
	// A typo like "file" instead of "files" must fail loudly, not pack
	// nothing; truncated JSON must fail too.
	dir := t.TempDir()
	p := writeManifest(t, dir, `{"file":[{"from":"x"}],"files":[{"from":"x"}]}`)
	if _, err := Load(p); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("want unknown-field error, got %v", err)
	}
	p = writeManifest(t, dir, `{"files": [`)
	if _, err := Load(p); err == nil {
		t.Fatal("want parse error, got nil")
	}
}

func TestLoadRejectsUnsupportedShell(t *testing.T) {
	dir := t.TempDir()
	p := writeManifest(t, dir, `{"shell":"fish","files":[{"from":"x"}]}`)
	if _, err := Load(p); err == nil || !strings.Contains(err.Error(), "not supported") {
		t.Fatalf("want shell error, got %v", err)
	}
}

func TestLoadRejectsBadBudgets(t *testing.T) {
	// Negative budgets are nonsense; budgets above ~100 KiB stop
	// fitting in a single argv string, so the config must be refused up
	// front, not fail later inside ssh with an opaque E2BIG.
	dir := t.TempDir()
	for budget, wantErr := range map[string]string{"-4": "positive", "512": "ceiling"} {
		p := writeManifest(t, dir, `{"budget_kb":`+budget+`,"files":[{"from":"x"}]}`)
		if _, err := Load(p); err == nil || !strings.Contains(err.Error(), wantErr) {
			t.Errorf("budget %s: want %q error, got %v", budget, wantErr, err)
		}
	}
}

func TestLoadRejectsMissingFiles(t *testing.T) {
	dir := t.TempDir()
	p := writeManifest(t, dir, `{"files":[]}`)
	if _, err := Load(p); err == nil || !strings.Contains(err.Error(), "at least one") {
		t.Fatalf("want files error, got %v", err)
	}
	p = writeManifest(t, dir, `{"files":[{"to":"x"}]}`)
	if _, err := Load(p); err == nil || !strings.Contains(err.Error(), `"from" is required`) {
		t.Fatalf("want from error, got %v", err)
	}
}

func TestLoadRejectsBadEnvAndExcludePatterns(t *testing.T) {
	dir := t.TempDir()
	p := writeManifest(t, dir, `{"files":[{"from":"x"}],"env":{"1BAD":"v"}}`)
	if _, err := Load(p); err == nil || !strings.Contains(err.Error(), "environment variable") {
		t.Fatalf("want env error, got %v", err)
	}
	p = writeManifest(t, dir, `{"files":[{"from":"x"}],"exclude":["[bad"]}`)
	if _, err := Load(p); err == nil || !strings.Contains(err.Error(), "bad pattern") {
		t.Fatalf("want pattern error, got %v", err)
	}
}

func TestResolveExpandsTildeAgainstInjectedHome(t *testing.T) {
	home := t.TempDir()
	touch(t, home, ".vimrc", "set nu\n")
	m := Manifest{Files: []Entry{{From: "~/.vimrc"}}}
	files, err := Resolve(m, t.TempDir(), home)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].Src != filepath.Join(home, ".vimrc") {
		t.Fatalf("unexpected resolution: %+v", files)
	}
	if files[0].Dest != ".vimrc" {
		t.Errorf("dest = %q, want .vimrc", files[0].Dest)
	}
	// Without a home directory, tilde expansion has to fail, not guess.
	if _, err := Resolve(m, t.TempDir(), ""); err == nil || !strings.Contains(err.Error(), "home directory unknown") {
		t.Fatalf("want home error, got %v", err)
	}
}

func TestResolveRelativeSourcesUseManifestDir(t *testing.T) {
	// Relative "from" paths anchor to the manifest's directory, not the
	// process working directory — that is what makes a checked-in
	// manifest portable.
	base := t.TempDir()
	touch(t, base, "duffelrc", "alias x=y\n")
	m := Manifest{Files: []Entry{{From: "duffelrc"}}}
	files, err := Resolve(m, base, "")
	if err != nil {
		t.Fatal(err)
	}
	if files[0].Src != filepath.Join(base, "duffelrc") {
		t.Fatalf("src = %q", files[0].Src)
	}
}

func TestResolveGlobMapsIntoDirectorySorted(t *testing.T) {
	base := t.TempDir()
	touch(t, base, "tools/b.sh", "b")
	touch(t, base, "tools/a.sh", "a")
	m := Manifest{Files: []Entry{{From: "tools/*.sh", To: "bin/"}}}
	files, err := Resolve(m, base, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 || files[0].Dest != "bin/a.sh" || files[1].Dest != "bin/b.sh" {
		t.Fatalf("unexpected dests: %+v", files)
	}
}

func TestResolveGlobMatchingNothingFails(t *testing.T) {
	m := Manifest{Files: []Entry{{From: "nope/*.conf"}}}
	if _, err := Resolve(m, t.TempDir(), ""); err == nil || !strings.Contains(err.Error(), "matched nothing") {
		t.Fatalf("want glob error, got %v", err)
	}
}

func TestResolveGlobIntoSingleNameFails(t *testing.T) {
	// Two matches cannot both become one destination file; the error
	// tells the user to end "to" with a slash.
	base := t.TempDir()
	touch(t, base, "a.sh", "a")
	touch(t, base, "b.sh", "b")
	m := Manifest{Files: []Entry{{From: "*.sh", To: "one.sh"}}}
	_, err := Resolve(m, base, "")
	if err == nil || !strings.Contains(err.Error(), `end it with "/"`) {
		t.Fatalf("want to-directory hint, got %v", err)
	}
}

func TestResolveRejectsDirectorySource(t *testing.T) {
	base := t.TempDir()
	touch(t, base, "conf/inner.txt", "x")
	m := Manifest{Files: []Entry{{From: "conf"}}}
	_, err := Resolve(m, base, "")
	if err == nil || !strings.Contains(err.Error(), "is a directory") {
		t.Fatalf("want directory error, got %v", err)
	}
}

func TestResolveRejectsForbiddenDestinations(t *testing.T) {
	// Escapes would land files outside the remote tempdir; .duffelrc is
	// where the generated wrapper lands, so a user file there would be
	// silently overwritten inside the bundle.
	base := t.TempDir()
	touch(t, base, "x", "x")
	for _, to := range []string{"../evil", "a/../../evil", "/etc/evil", RCName} {
		m := Manifest{Files: []Entry{{From: "x", To: to}}}
		if _, err := Resolve(m, base, ""); err == nil {
			t.Errorf("to=%q: want rejection, got nil", to)
		}
	}
}

func TestResolveAppliesExcludeToDestAndBasename(t *testing.T) {
	base := t.TempDir()
	touch(t, base, "keep.sh", "k")
	touch(t, base, "skip.bak", "s")
	touch(t, base, "notes/skip.tmp", "s")
	m := Manifest{
		Files:   []Entry{{From: "keep.sh"}, {From: "skip.bak"}, {From: "notes/skip.tmp", To: "notes/"}},
		Exclude: []string{"*.bak", "notes/*"},
	}
	files, err := Resolve(m, base, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].Dest != "keep.sh" {
		t.Fatalf("unexpected survivors: %+v", files)
	}
	// Excluding everything must fail, not emit an empty duffel.
	all := Manifest{Files: []Entry{{From: "skip.bak"}}, Exclude: []string{"*.bak"}}
	if _, err := Resolve(all, base, ""); err == nil || !strings.Contains(err.Error(), "zero files") {
		t.Fatalf("want zero-files error, got %v", err)
	}
}

func TestResolveDuplicateDestinationFails(t *testing.T) {
	base := t.TempDir()
	touch(t, base, "a/rc", "a")
	touch(t, base, "b/rc", "b")
	m := Manifest{Files: []Entry{{From: "a/rc"}, {From: "b/rc"}}}
	_, err := Resolve(m, base, "")
	if err == nil || !strings.Contains(err.Error(), "already claimed") {
		t.Fatalf("want duplicate error, got %v", err)
	}
}

func TestResolveSortsByDestination(t *testing.T) {
	base := t.TempDir()
	touch(t, base, "zz", "z")
	touch(t, base, "aa", "a")
	m := Manifest{Files: []Entry{{From: "zz"}, {From: "aa"}}}
	files, err := Resolve(m, base, "")
	if err != nil {
		t.Fatal(err)
	}
	if files[0].Dest != "aa" || files[1].Dest != "zz" {
		t.Fatalf("not sorted: %+v", files)
	}
}

func TestResolveCarriesAllowSecretsFlag(t *testing.T) {
	base := t.TempDir()
	touch(t, base, "token.txt", "t")
	m := Manifest{Files: []Entry{{From: "token.txt", AllowSecrets: true}}}
	files, err := Resolve(m, base, "")
	if err != nil {
		t.Fatal(err)
	}
	if !files[0].AllowSecrets {
		t.Fatal("allow_secrets flag lost during resolution")
	}
}

func TestHasEntryFindsTheEntryFile(t *testing.T) {
	m := Manifest{Entry: "duffelrc"}
	files := []File{{Dest: "aliases.sh"}, {Dest: "duffelrc"}}
	if !HasEntry(m, files) {
		t.Fatal("entry present but not found")
	}
	if HasEntry(m, files[:1]) {
		t.Fatal("entry absent but reported found")
	}
}

func TestSortedEnvIsDeterministic(t *testing.T) {
	m := Manifest{Env: map[string]string{"ZED": "1", "ALPHA": "2", "MID": "3"}}
	got := SortedEnv(m)
	want := []string{"ALPHA=2", "MID=3", "ZED=1"}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}
