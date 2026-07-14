# Contributing to dotduffel

Issues, discussions and pull requests are all welcome.

## Getting started

You need Go 1.22 or newer; there are no other dependencies of any kind.

```bash
git clone https://github.com/JaydenCJ/dotduffel.git
cd dotduffel
go build ./...
go test ./...
bash scripts/smoke.sh
```

`scripts/smoke.sh` builds the binary and drives the full lifecycle —
init → pack → local session → cleanup check → secret-guard refusal →
transport argv shapes; it must finish by printing `SMOKE OK`.

## Before you open a pull request

1. `gofmt -l .` reports nothing (formatting is enforced).
2. `go vet ./...` passes with no findings.
3. `go test ./...` passes (all 90 tests).
4. `bash scripts/smoke.sh` prints `SMOKE OK`.
5. Add tests for behavior changes; keep logic in pure, unit-testable
   packages (`manifest`, `bundle`, `guard`, `bootstrap`, `remote`) rather
   than in the CLI layer.

## Ground rules

- Zero runtime dependencies is a core feature: the `go.mod` require list
  stays empty. Adding a dependency needs strong justification in the PR.
- No network calls, ever — dotduffel reads local files and spawns the
  transport you asked for, nothing else. No telemetry.
- The bootstrap script must stay strictly POSIX sh and must never `exec`
  the user's shell: the EXIT trap is the cleanup guarantee.
- Bundle output must stay byte-reproducible; anything that would make two
  packs of the same manifest differ is a bug.
- New secret-guard rules must be conservative: a default rule needs an
  effectively-zero false-positive rate on ordinary dotfiles, otherwise it
  does not ship.
- Code comments and doc comments are written in English.

## Reporting bugs

Please include the output of `dotduffel --version`, your `duffel.json`
(redacted as needed), the failing command with `--print` output where it
applies, and — for remote issues — the target's `sh`, `tar` and `base64`
flavors (`busybox`, GNU, BSD).

## Security

Please do not open public issues for security problems; use GitHub's
private vulnerability reporting on this repository instead.
