// Package bundle turns resolved manifest entries into a deterministic
// tar.gz payload.
//
// Determinism is a feature, not an accident: timestamps are pinned to
// the epoch, ownership is zeroed, modes are normalized to 0644/0755,
// and members are sorted by destination — so packing the same inputs
// twice yields byte-identical output. That makes bundles diffable,
// cacheable, and safe to check into scripts without spurious churn.
package bundle

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/JaydenCJ/dotduffel/internal/manifest"
)

// File is one in-memory bundle member. Data is the full content; Mode
// is already normalized to 0644 or 0755.
type File struct {
	Dest         string
	Mode         int64
	Data         []byte
	AllowSecrets bool
}

// MaxFileSize rejects any single member above 256 KiB before it is even
// read into memory: a file that large is never a dotfile, and it would
// blow the argv budget anyway with a far less helpful error.
const MaxFileSize = 256 * 1024

// Load reads every resolved source file into memory, following
// symlinks (a symlinked ~/.vimrc should pack its content, not a
// dangling link on the remote side).
func Load(resolved []manifest.File) ([]File, error) {
	out := make([]File, 0, len(resolved))
	for _, r := range resolved {
		if r.Size > MaxFileSize {
			return nil, fmt.Errorf("%s: %d bytes exceeds the %d KiB per-file cap — dotduffel packs dotfiles, not assets", r.Src, r.Size, MaxFileSize/1024)
		}
		data, err := os.ReadFile(r.Src)
		if err != nil {
			return nil, err
		}
		out = append(out, File{
			Dest:         r.Dest,
			Mode:         normalizeMode(r.Mode.Perm()),
			Data:         data,
			AllowSecrets: r.AllowSecrets,
		})
	}
	return out, nil
}

// Synthetic wraps generated content (the rc wrapper) as a bundle member.
func Synthetic(dest string, data []byte) File {
	return File{Dest: dest, Mode: 0o644, Data: data}
}

// Build produces the deterministic tar.gz payload. Members are written
// sorted by destination with epoch mtimes and zeroed ownership; gzip
// runs at best compression with no name or timestamp in its header.
func Build(files []File) ([]byte, error) {
	if len(files) == 0 {
		return nil, fmt.Errorf("nothing to pack")
	}
	sorted := make([]File, len(files))
	copy(sorted, files)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Dest < sorted[j].Dest })
	for i := 1; i < len(sorted); i++ {
		if sorted[i].Dest == sorted[i-1].Dest {
			return nil, fmt.Errorf("duplicate destination %q", sorted[i].Dest)
		}
	}

	var buf bytes.Buffer
	gz, err := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	if err != nil {
		return nil, err
	}
	tw := tar.NewWriter(gz)
	for _, f := range sorted {
		hdr := &tar.Header{
			Name:    f.Dest,
			Mode:    f.Mode,
			Size:    int64(len(f.Data)),
			ModTime: time.Unix(0, 0).UTC(),
			Uid:     0,
			Gid:     0,
			Format:  tar.FormatUSTAR,
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return nil, fmt.Errorf("%s: %w", f.Dest, err)
		}
		if _, err := tw.Write(f.Data); err != nil {
			return nil, fmt.Errorf("%s: %w", f.Dest, err)
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// RawSize is the total uncompressed byte count of all members.
func RawSize(files []File) int {
	n := 0
	for _, f := range files {
		n += len(f.Data)
	}
	return n
}

// Largest returns up to n members sorted by descending raw size — used
// to explain budget overflows in terms the user can act on.
func Largest(files []File, n int) []File {
	sorted := make([]File, len(files))
	copy(sorted, files)
	sort.Slice(sorted, func(i, j int) bool {
		if len(sorted[i].Data) != len(sorted[j].Data) {
			return len(sorted[i].Data) > len(sorted[j].Data)
		}
		return sorted[i].Dest < sorted[j].Dest
	})
	if len(sorted) > n {
		sorted = sorted[:n]
	}
	return sorted
}

// normalizeMode keeps exactly one bit of the source mode: whether the
// owner could execute it. Everything else (group/world bits, setuid,
// sticky) is host noise that would make bundles machine-dependent.
func normalizeMode(perm os.FileMode) int64 {
	if perm&0o100 != 0 {
		return 0o755
	}
	return 0o644
}
