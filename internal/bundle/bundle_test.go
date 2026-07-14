// Tests for the deterministic bundle builder: reproducibility is the
// contract here, so several tests assert exact bytes and header fields
// rather than just "it did not error".
package bundle

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JaydenCJ/dotduffel/internal/manifest"
)

func member(dest, data string) File {
	return File{Dest: dest, Mode: 0o644, Data: []byte(data)}
}

// untar decodes a built payload back into dest→header/content pairs.
func untar(t *testing.T, tgz []byte) map[string]*tar.Header {
	t.Helper()
	gz, err := gzip.NewReader(bytes.NewReader(tgz))
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	out := map[string]*tar.Header{}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		out[hdr.Name] = hdr
	}
	return out
}

func TestBuildIsByteReproducible(t *testing.T) {
	// Same members in, identical bytes out — twice in a row. This is
	// what makes bundles diffable and cacheable.
	files := []File{member("b", "bbb"), member("a", "aaa"), {Dest: "c", Mode: 0o755, Data: []byte("ccc")}}
	one, err := Build(files)
	if err != nil {
		t.Fatal(err)
	}
	two, err := Build(files)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(one, two) {
		t.Fatal("two builds of the same input differ")
	}
	// The caller's slice order must not leak into the output either.
	shuffled, err := Build([]File{{Dest: "c", Mode: 0o755, Data: []byte("ccc")}, member("a", "aaa"), member("b", "bbb")})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(one, shuffled) {
		t.Fatal("member order changed the output bytes")
	}
}

func TestBuildRoundTripsContent(t *testing.T) {
	tgz, err := Build([]File{member("dir/deep.conf", "k = v\n"), member("top", "hello")})
	if err != nil {
		t.Fatal(err)
	}
	gz, err := gzip.NewReader(bytes.NewReader(tgz))
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	got := map[string]string{}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		data, _ := io.ReadAll(tr)
		got[hdr.Name] = string(data)
	}
	if got["dir/deep.conf"] != "k = v\n" || got["top"] != "hello" {
		t.Fatalf("round trip mismatch: %v", got)
	}
}

func TestBuildPinsTimestampsAndOwnership(t *testing.T) {
	tgz, err := Build([]File{member("rc", "x")})
	if err != nil {
		t.Fatal(err)
	}
	hdr := untar(t, tgz)["rc"]
	if hdr.ModTime.Unix() != 0 {
		t.Errorf("mtime = %v, want epoch", hdr.ModTime)
	}
	if hdr.Uid != 0 || hdr.Gid != 0 {
		t.Errorf("ownership = %d:%d, want 0:0", hdr.Uid, hdr.Gid)
	}
	// gzip header must not embed a wall-clock timestamp either.
	if tgz[4] != 0 || tgz[5] != 0 || tgz[6] != 0 || tgz[7] != 0 {
		t.Error("gzip MTIME field is set; output would churn between packs")
	}
}

func TestBuildKeepsExecutableBitOnly(t *testing.T) {
	tgz, err := Build([]File{
		{Dest: "script", Mode: 0o755, Data: []byte("#!/bin/sh\n")},
		{Dest: "plain", Mode: 0o644, Data: []byte("x")},
	})
	if err != nil {
		t.Fatal(err)
	}
	hdrs := untar(t, tgz)
	if hdrs["script"].Mode != 0o755 {
		t.Errorf("script mode = %o, want 755", hdrs["script"].Mode)
	}
	if hdrs["plain"].Mode != 0o644 {
		t.Errorf("plain mode = %o, want 644", hdrs["plain"].Mode)
	}
}

func TestBuildRejectsEmptyInput(t *testing.T) {
	if _, err := Build(nil); err == nil || !strings.Contains(err.Error(), "nothing to pack") {
		t.Fatalf("want empty error, got %v", err)
	}
}

func TestBuildRejectsDuplicateDest(t *testing.T) {
	_, err := Build([]File{member("rc", "a"), member("rc", "b")})
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("want duplicate error, got %v", err)
	}
}

func TestLoadNormalizesModes(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "tool.sh")
	os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o700) // 0700, not 0755
	plain := filepath.Join(dir, "rc")
	os.WriteFile(plain, []byte("x"), 0o600) // 0600, not 0644
	files, err := Load([]manifest.File{
		{Src: exe, Dest: "tool.sh", Mode: 0o700, Size: 10},
		{Src: plain, Dest: "rc", Mode: 0o600, Size: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	if files[0].Mode != 0o755 || files[1].Mode != 0o644 {
		t.Fatalf("modes = %o/%o, want 755/644", files[0].Mode, files[1].Mode)
	}
}

func TestLoadFollowsSymlinks(t *testing.T) {
	// A symlinked ~/.vimrc must pack its content — a link target path
	// would dangle on the remote machine.
	dir := t.TempDir()
	real := filepath.Join(dir, "real.vimrc")
	os.WriteFile(real, []byte("set nu\n"), 0o644)
	link := filepath.Join(dir, ".vimrc")
	if err := os.Symlink(real, link); err != nil {
		t.Skip("symlinks unavailable:", err)
	}
	files, err := Load([]manifest.File{{Src: link, Dest: ".vimrc", Mode: 0o644, Size: 7}})
	if err != nil {
		t.Fatal(err)
	}
	if string(files[0].Data) != "set nu\n" {
		t.Fatalf("data = %q", files[0].Data)
	}
}

func TestLoadRejectsOversizeFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "huge.bin")
	os.WriteFile(p, []byte("x"), 0o644)
	_, err := Load([]manifest.File{{Src: p, Dest: "huge.bin", Mode: 0o644, Size: MaxFileSize + 1}})
	if err == nil || !strings.Contains(err.Error(), "per-file cap") {
		t.Fatalf("want cap error, got %v", err)
	}
}

func TestLargestAndRawSizeAccounting(t *testing.T) {
	files := []File{
		member("small", "1"),
		member("big-b", "xxxxxxxx"),
		member("big-a", "yyyyyyyy"),
		member("mid", "zzzz"),
	}
	top := Largest(files, 3)
	if len(top) != 3 || top[0].Dest != "big-a" || top[1].Dest != "big-b" || top[2].Dest != "mid" {
		t.Fatalf("unexpected order: %v %v %v", top[0].Dest, top[1].Dest, top[2].Dest)
	}
	if got := RawSize(files); got != 21 {
		t.Fatalf("RawSize = %d, want 21", got)
	}
}
