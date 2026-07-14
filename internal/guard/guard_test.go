// Tests for the secret guard. The failure cases matter most here: a
// missed private key ends up in a tempdir on a machine the user does
// not control, so detection tests use realistic material, and the
// false-positive tests pin down what must NOT trip the guard.
package guard

import (
	"strings"
	"testing"

	"github.com/JaydenCJ/dotduffel/internal/bundle"
)

func file(dest, data string) bundle.File {
	return bundle.File{Dest: dest, Mode: 0o644, Data: []byte(data)}
}

func rules(fs []Finding) []string {
	out := make([]string, len(fs))
	for i, f := range fs {
		out[i] = f.Rule
	}
	return out
}

func TestDetectsPEMPrivateKeyVariants(t *testing.T) {
	for _, header := range []string{
		"-----BEGIN RSA PRIVATE KEY-----",
		"-----BEGIN OPENSSH PRIVATE KEY-----",
		"-----BEGIN EC PRIVATE KEY-----",
		"-----BEGIN PRIVATE KEY-----",           // unlabelled PKCS#8
		"-----BEGIN PGP PRIVATE KEY BLOCK-----", // GPG export
		"-----BEGIN ENCRYPTED PRIVATE KEY-----", // passphrase-protected
	} {
		fs := Scan([]bundle.File{file("notes.txt", "prefix\n"+header+"\nAAAA\n")})
		if len(fs) != 1 || fs[0].Rule != "private-key" {
			t.Errorf("%s: findings = %v", header, fs)
		}
	}
}

func TestPublicKeyMaterialPasses(t *testing.T) {
	// Public halves are routinely packed (authorized_keys workflows)
	// and must not trip the guard.
	for _, content := range []string{
		"-----BEGIN PUBLIC KEY-----\nAAAA\n-----END PUBLIC KEY-----\n",
		"-----BEGIN CERTIFICATE-----\nAAAA\n-----END CERTIFICATE-----\n",
		"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIISH4jd6dMB4dotduffe1demo0key user@example.test\n",
	} {
		if fs := Scan([]bundle.File{file("keys.txt", content)}); len(fs) != 0 {
			t.Errorf("%q: unexpected findings %v", content[:24], fs)
		}
	}
}

func TestDetectsAWSAccessKeyIDButNotLookalikes(t *testing.T) {
	fs := Scan([]bundle.File{file("env.sh", "export AWS_ACCESS_KEY_ID=AKIAIOSFODNN7EXAMPLE\n")})
	if len(fs) != 1 || fs[0].Rule != "aws-access-key" {
		t.Fatalf("findings = %v", fs)
	}
	// Lowercase, wrong length and embedded-in-word forms are not key IDs.
	for _, s := range []string{
		"akiaiosfodnn7example\n",
		"AKIA1234\n",
		"XAKIAIOSFODNN7EXAMPLEX\n",
	} {
		if fs := Scan([]bundle.File{file("env.sh", s)}); len(fs) != 0 {
			t.Errorf("%q: unexpected findings %v", s, fs)
		}
	}
}

func TestDetectsGitHubTokens(t *testing.T) {
	for _, tok := range []string{
		"ghp_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		"github_pat_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
	} {
		fs := Scan([]bundle.File{file(".gitconfig", "token = "+tok+"\n")})
		if len(fs) != 1 || fs[0].Rule != "github-token" {
			t.Errorf("%s...: findings = %v", tok[:10], fs)
		}
	}
	// "ghp_test" in prose or example configs is not a real token.
	if fs := Scan([]bundle.File{file("README", "set token to ghp_test here\n")}); len(fs) != 0 {
		t.Fatalf("unexpected findings %v", fs)
	}
}

func TestDetectsSlackToken(t *testing.T) {
	fs := Scan([]bundle.File{file("slack.sh", "export SLACK_TOKEN=xoxb-AAAAAAAAAA-EXAMPLE\n")})
	if len(fs) != 1 || fs[0].Rule != "slack-token" {
		t.Fatalf("findings = %v", fs)
	}
}

func TestDetectsNpmAuthToken(t *testing.T) {
	fs := Scan([]bundle.File{file(".npmrc", "//registry.example.test/:_authToken=abc123\n")})
	if len(fs) != 1 || fs[0].Rule != "npm-token" {
		t.Fatalf("findings = %v", fs)
	}
}

func TestSensitiveBasenamesRefusedWhateverTheContent(t *testing.T) {
	for _, name := range []string{"id_rsa", "id_ed25519", ".netrc", ".pgpass", "aws/credentials"} {
		fs := Scan([]bundle.File{file(name, "innocuous text\n")})
		if len(fs) != 1 || fs[0].Rule != "sensitive-name" {
			t.Errorf("%s: findings = %v", name, fs)
		}
	}
}

func TestSensitiveSuffixesRefused(t *testing.T) {
	for _, name := range []string{"server.pem", "client.p12", "signing.pfx"} {
		fs := Scan([]bundle.File{file(name, "text\n")})
		if len(fs) != 1 || fs[0].Rule != "sensitive-name" {
			t.Errorf("%s: findings = %v", name, fs)
		}
	}
}

func TestPublicKeyFilenamesPass(t *testing.T) {
	// id_rsa.pub is exactly the file people DO want to carry around.
	if fs := Scan([]bundle.File{file("id_rsa.pub", "ssh-rsa AAAA user@example.test\n")}); len(fs) != 0 {
		t.Fatalf("unexpected findings %v", fs)
	}
}

func TestDetectsBinaryContentAndSkipsContentRules(t *testing.T) {
	fs := Scan([]bundle.File{{Dest: "tool", Mode: 0o755, Data: []byte("\x7fELF\x00\x00\x01")}})
	if len(fs) != 1 || fs[0].Rule != "binary" {
		t.Fatalf("findings = %v", fs)
	}
	// One clear "binary" finding beats a pile of regex noise from
	// random bytes — content rules must not also fire.
	data := append([]byte{0}, []byte("AKIAIOSFODNN7EXAMPLE")...)
	fs = Scan([]bundle.File{{Dest: "blob", Mode: 0o644, Data: data}})
	if len(fs) != 1 || fs[0].Rule != "binary" {
		t.Fatalf("findings = %v", fs)
	}
}

func TestAllowSecretsSkipsEveryRule(t *testing.T) {
	f := file("id_rsa", "-----BEGIN RSA PRIVATE KEY-----\nAAAA\n")
	f.AllowSecrets = true
	if fs := Scan([]bundle.File{f}); len(fs) != 0 {
		t.Fatalf("allow_secrets ignored: %v", fs)
	}
}

func TestFindingReportsLineNumberAndReadsWell(t *testing.T) {
	content := "line one\nline two\nAKIAIOSFODNN7EXAMPLE\n"
	fs := Scan([]bundle.File{file("env.sh", content)})
	if len(fs) != 1 || !strings.Contains(fs[0].Detail, "line 3") {
		t.Fatalf("findings = %v", fs)
	}
	want := "env.sh: contains an AWS access key ID at line 3 (aws-access-key)"
	if fs[0].String() != want {
		t.Fatalf("String() = %q", fs[0].String())
	}
}

func TestMultipleFindingsAcrossFiles(t *testing.T) {
	fs := Scan([]bundle.File{
		file("a.sh", "AKIAIOSFODNN7EXAMPLE\n"),
		file("clean.sh", "alias ll='ls -l'\n"),
		file("id_rsa", "text\n"),
	})
	got := rules(fs)
	if len(got) != 2 || got[0] != "aws-access-key" || got[1] != "sensitive-name" {
		t.Fatalf("rules = %v", got)
	}
}

func TestRealisticDotfileCorpusPasses(t *testing.T) {
	// The guard exists to catch mistakes, not to make normal dotfiles
	// impossible to pack. A representative clean corpus must be silent.
	fs := Scan([]bundle.File{
		file("duffelrc", "# entry\n. \"$DUFFEL_DIR/aliases.sh\"\nPS1='(duffel) '\n"),
		file("aliases.sh", "alias ll='ls -alF'\nalias gs='git status'\n"),
		file(".vimrc", "set number\nset expandtab shiftwidth=4\n"),
		file(".gitconfig", "[user]\n\tname = Jayden\n[alias]\n\tlg = log --oneline\n"),
		file(".inputrc", "set editing-mode vi\n"),
		file("bin/serve.sh", "#!/bin/sh\npython3 -m http.server --bind 127.0.0.1 8080\n"),
	})
	if len(fs) != 0 {
		t.Fatalf("clean corpus tripped the guard: %v", fs)
	}
}
