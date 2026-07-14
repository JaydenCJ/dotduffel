// Package guard scans bundle members for material that must never ride
// along to a shared box: private keys, API tokens, credential files.
//
// The duffel lands in a world-readable-by-root tempdir on machines you
// do not control — CI runners, shared jump hosts, other people's
// containers — so the scanner is deliberately conservative and fails
// closed. Every rule can be overridden per file with
// "allow_secrets": true in the manifest, which keeps the override
// visible in code review instead of hidden in a flag someone typed once.
package guard

import (
	"bytes"
	"fmt"
	"path"
	"regexp"
	"strings"

	"github.com/JaydenCJ/dotduffel/internal/bundle"
)

// Finding is one reason a file was refused.
type Finding struct {
	Dest   string
	Rule   string
	Detail string
}

func (f Finding) String() string {
	return fmt.Sprintf("%s: %s (%s)", f.Dest, f.Detail, f.Rule)
}

// contentRule matches secret material inside a file.
type contentRule struct {
	name   string
	re     *regexp.Regexp
	detail string
}

var contentRules = []contentRule{
	{
		name: "private-key",
		// Any PEM private-key block: RSA, EC, DSA, OPENSSH, PGP, or the
		// unlabelled PKCS#8 form. Public keys and certificates do not match.
		re:     regexp.MustCompile(`-----BEGIN [A-Z0-9 ]*PRIVATE KEY( BLOCK)?-----`),
		detail: "contains a PEM private-key block",
	},
	{
		name:   "aws-access-key",
		re:     regexp.MustCompile(`\b(AKIA|ASIA)[0-9A-Z]{16}\b`),
		detail: "contains an AWS access key ID",
	},
	{
		name:   "github-token",
		re:     regexp.MustCompile(`\b(gh[pousr]_[A-Za-z0-9]{36,}|github_pat_[A-Za-z0-9_]{22,})\b`),
		detail: "contains a GitHub token",
	},
	{
		name:   "slack-token",
		re:     regexp.MustCompile(`\bxox[abprs]-[0-9A-Za-z-]{10,}\b`),
		detail: "contains a Slack token",
	},
	{
		name:   "npm-token",
		re:     regexp.MustCompile(`(?m)^\s*//.*:_authToken\s*=`),
		detail: "contains an npm registry auth token",
	},
}

// sensitiveNames are exact basenames that are credential stores by
// convention, whatever their content looks like today.
var sensitiveNames = map[string]string{
	"id_rsa":      "an SSH private key by convention",
	"id_dsa":      "an SSH private key by convention",
	"id_ecdsa":    "an SSH private key by convention",
	"id_ed25519":  "an SSH private key by convention",
	".netrc":      "a plaintext credentials file",
	".pgpass":     "a PostgreSQL password file",
	"credentials": "an AWS-style credentials file",
}

// sensitiveSuffixes are extensions that overwhelmingly hold key material.
var sensitiveSuffixes = []string{".pem", ".p12", ".pfx", ".keystore"}

// binaryProbe is how many leading bytes are checked for NUL. Dotfiles
// are text; a binary here is almost always a mistake (a compiled tool,
// an image) that would also wreck the size budget.
const binaryProbe = 8192

// Scan checks every member and returns all findings for files that did
// not opt out via allow_secrets. An empty result means the bundle is
// clear to leave the machine.
func Scan(files []bundle.File) []Finding {
	var out []Finding
	for _, f := range files {
		if f.AllowSecrets {
			continue
		}
		out = append(out, scanOne(f)...)
	}
	return out
}

func scanOne(f bundle.File) []Finding {
	var out []Finding
	base := path.Base(f.Dest)
	if why, ok := sensitiveNames[base]; ok {
		out = append(out, Finding{f.Dest, "sensitive-name", "filename is " + why})
	}
	for _, suf := range sensitiveSuffixes {
		if strings.HasSuffix(base, suf) && base != suf {
			out = append(out, Finding{f.Dest, "sensitive-name", "*" + suf + " files usually hold key material"})
			break
		}
	}
	if isBinary(f.Data) {
		out = append(out, Finding{f.Dest, "binary", "looks like a binary file, not a dotfile"})
		// Content rules on binary data would only produce noise.
		return out
	}
	for _, r := range contentRules {
		if loc := r.re.FindIndex(f.Data); loc != nil {
			out = append(out, Finding{f.Dest, r.name, fmt.Sprintf("%s at line %d", r.detail, lineOf(f.Data, loc[0]))})
		}
	}
	return out
}

func isBinary(data []byte) bool {
	probe := data
	if len(probe) > binaryProbe {
		probe = probe[:binaryProbe]
	}
	return bytes.IndexByte(probe, 0) >= 0
}

func lineOf(data []byte, offset int) int {
	return bytes.Count(data[:offset], []byte{'\n'}) + 1
}
