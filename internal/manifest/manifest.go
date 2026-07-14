// Package manifest loads and resolves duffel.json — the file that says
// which dotfiles travel and where they land inside the bundle.
//
// Loading and resolving are separate, pure steps: Load parses and
// validates the JSON strictly (unknown keys are errors, so typos fail
// loudly instead of silently packing nothing), and Resolve expands
// tildes and globs against an explicit home directory and base
// directory, which is what makes every test hermetic.
package manifest

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// RCName is the reserved destination of the generated rc wrapper that
// dotduffel injects into every bundle. User files may not claim it.
const RCName = ".duffelrc"

// DefaultBudgetKB caps the generated bootstrap script at 64 KiB. The
// script travels inside a single ssh/exec argument, and 64 KiB stays
// comfortably below common ARG_MAX limits (Linux caps a single argv
// string at 128 KiB). See docs/bootstrap-protocol.md for the rationale.
const DefaultBudgetKB = 64

// MaxBudgetKB is the hard ceiling for budget_kb. Beyond ~100 KiB a
// single argv string stops being portable, so we refuse the config
// rather than let ssh fail with an opaque E2BIG later.
const MaxBudgetKB = 100

// Entry is one files[] element: a source pattern and an optional
// destination inside the bundle.
type Entry struct {
	From         string `json:"from"`
	To           string `json:"to,omitempty"`
	AllowSecrets bool   `json:"allow_secrets,omitempty"`
}

// Manifest is the parsed duffel.json.
type Manifest struct {
	Entry    string            `json:"entry,omitempty"`
	Shell    string            `json:"shell,omitempty"`
	BudgetKB int               `json:"budget_kb,omitempty"`
	Files    []Entry           `json:"files"`
	Exclude  []string          `json:"exclude,omitempty"`
	Env      map[string]string `json:"env,omitempty"`
}

// File is one resolved bundle member: an absolute source path on this
// machine and a clean, relative destination inside the duffel.
type File struct {
	Src          string
	Dest         string
	Mode         fs.FileMode
	Size         int64
	AllowSecrets bool
}

var envKeyRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Load reads and validates a manifest file. Unknown keys, bad shells,
// out-of-range budgets and malformed env names are all hard errors:
// a manifest that half-works would pack the wrong thing silently.
func Load(mpath string) (Manifest, error) {
	f, err := os.Open(mpath)
	if err != nil {
		return Manifest{}, err
	}
	defer f.Close()

	dec := json.NewDecoder(f)
	dec.DisallowUnknownFields()
	var m Manifest
	if err := dec.Decode(&m); err != nil {
		return Manifest{}, fmt.Errorf("%s: %w", mpath, err)
	}
	if err := validate(&m); err != nil {
		return Manifest{}, fmt.Errorf("%s: %w", mpath, err)
	}
	return m, nil
}

func validate(m *Manifest) error {
	if m.Entry == "" {
		m.Entry = "duffelrc"
	}
	if _, err := cleanDest(m.Entry); err != nil {
		return fmt.Errorf("entry: %w", err)
	}
	switch m.Shell {
	case "":
		m.Shell = "bash"
	case "bash", "sh":
	default:
		return fmt.Errorf("shell: %q is not supported (use \"bash\" or \"sh\")", m.Shell)
	}
	switch {
	case m.BudgetKB == 0:
		m.BudgetKB = DefaultBudgetKB
	case m.BudgetKB < 0:
		return fmt.Errorf("budget_kb: must be positive, got %d", m.BudgetKB)
	case m.BudgetKB > MaxBudgetKB:
		return fmt.Errorf("budget_kb: %d exceeds the %d KiB argv-portability ceiling", m.BudgetKB, MaxBudgetKB)
	}
	if len(m.Files) == 0 {
		return fmt.Errorf("files: at least one entry is required")
	}
	for i, e := range m.Files {
		if strings.TrimSpace(e.From) == "" {
			return fmt.Errorf("files[%d]: \"from\" is required", i)
		}
	}
	for _, pat := range m.Exclude {
		if _, err := path.Match(pat, "probe"); err != nil {
			return fmt.Errorf("exclude: bad pattern %q", pat)
		}
	}
	for k := range m.Env {
		if !envKeyRe.MatchString(k) {
			return fmt.Errorf("env: %q is not a valid environment variable name", k)
		}
	}
	return nil
}

// Resolve expands every files[] entry against baseDir (the manifest's
// directory, for relative sources) and home (for ~/ sources), applies
// the exclude patterns, and returns the members sorted by destination.
func Resolve(m Manifest, baseDir, home string) ([]File, error) {
	var out []File
	seen := map[string]string{} // dest -> from pattern that claimed it
	for i, e := range m.Files {
		src, err := expandSource(e.From, baseDir, home)
		if err != nil {
			return nil, fmt.Errorf("files[%d] %q: %w", i, e.From, err)
		}
		matches, err := expandGlob(src, e.From)
		if err != nil {
			return nil, fmt.Errorf("files[%d] %q: %w", i, e.From, err)
		}
		if len(matches) > 1 && e.To != "" && !strings.HasSuffix(e.To, "/") {
			return nil, fmt.Errorf("files[%d] %q: matches %d files but \"to\" (%q) is a single name — end it with \"/\" to map into a directory", i, e.From, len(matches), e.To)
		}
		for _, match := range matches {
			info, err := os.Stat(match)
			if err != nil {
				return nil, fmt.Errorf("files[%d] %q: %w", i, e.From, err)
			}
			if info.IsDir() {
				return nil, fmt.Errorf("files[%d] %q: %s is a directory — pack its contents with %q instead", i, e.From, match, e.From+"/*")
			}
			dest, err := destFor(e.To, match)
			if err != nil {
				return nil, fmt.Errorf("files[%d] %q: %w", i, e.From, err)
			}
			if excluded(m.Exclude, dest) {
				continue
			}
			if prev, dup := seen[dest]; dup {
				return nil, fmt.Errorf("files[%d] %q: destination %q already claimed by %q", i, e.From, dest, prev)
			}
			seen[dest] = e.From
			out = append(out, File{
				Src:          match,
				Dest:         dest,
				Mode:         info.Mode(),
				Size:         info.Size(),
				AllowSecrets: e.AllowSecrets,
			})
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("manifest resolved to zero files (everything excluded?)")
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Dest < out[j].Dest })
	return out, nil
}

// HasEntry reports whether the resolved set contains the manifest's
// entry file — the one thing a duffel cannot work without.
func HasEntry(m Manifest, files []File) bool {
	for _, f := range files {
		if f.Dest == m.Entry {
			return true
		}
	}
	return false
}

func expandSource(from, baseDir, home string) (string, error) {
	switch {
	case from == "~" || strings.HasPrefix(from, "~/"):
		if home == "" {
			return "", fmt.Errorf("cannot expand ~: home directory unknown")
		}
		return filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(from, "~"), "/")), nil
	case strings.HasPrefix(from, "~"):
		return "", fmt.Errorf("~user expansion is not supported")
	case filepath.IsAbs(from):
		return from, nil
	default:
		return filepath.Join(baseDir, from), nil
	}
}

func expandGlob(src, orig string) ([]string, error) {
	if !strings.ContainsAny(src, "*?[") {
		if _, err := os.Stat(src); err != nil {
			return nil, err
		}
		return []string{src}, nil
	}
	matches, err := filepath.Glob(src)
	if err != nil {
		return nil, fmt.Errorf("bad glob pattern")
	}
	if len(matches) == 0 {
		return nil, fmt.Errorf("glob matched nothing")
	}
	sort.Strings(matches)
	return matches, nil
}

func destFor(to, match string) (string, error) {
	base := filepath.Base(match)
	switch {
	case to == "":
		return cleanDest(base)
	case strings.HasSuffix(to, "/"):
		return cleanDest(path.Join(to, base))
	default:
		return cleanDest(to)
	}
}

// cleanDest normalizes a destination and rejects anything that could
// escape the remote tempdir: absolute paths, .. traversal, and the
// reserved rc-wrapper name.
func cleanDest(d string) (string, error) {
	d = strings.TrimSpace(d)
	if d == "" {
		return "", fmt.Errorf("empty destination")
	}
	if strings.HasPrefix(d, "/") {
		return "", fmt.Errorf("destination %q must be relative", d)
	}
	c := path.Clean(d)
	if c == "." || c == ".." || strings.HasPrefix(c, "../") {
		return "", fmt.Errorf("destination %q escapes the bundle", d)
	}
	if c == RCName {
		return "", fmt.Errorf("destination %q is reserved for the generated rc wrapper", RCName)
	}
	return c, nil
}

func excluded(patterns []string, dest string) bool {
	base := path.Base(dest)
	for _, pat := range patterns {
		if ok, _ := path.Match(pat, dest); ok {
			return true
		}
		if ok, _ := path.Match(pat, base); ok {
			return true
		}
	}
	return false
}

// SortedEnv returns the manifest's env map as deterministic KEY=VALUE
// pairs, sorted by key, so generated files never churn between packs.
func SortedEnv(m Manifest) []string {
	keys := make([]string, 0, len(m.Env))
	for k := range m.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, k+"="+m.Env[k])
	}
	return out
}
