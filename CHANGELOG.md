# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.1.0] - 2026-07-13

### Added

- `duffel.json` manifest: `~/` and glob sources, directory mappings via a
  trailing `/`, exclude patterns, env pins, strict parsing (unknown keys
  rejected), and destinations validated against tempdir escapes.
- Deterministic bundle builder: epoch mtimes, zeroed ownership, modes
  normalized to 0644/0755, sorted members, timestamp-free gzip — the same
  manifest packs to byte-identical output on every run.
- Secret guard that fails closed before anything leaves the machine:
  PEM private-key blocks, AWS access key IDs, GitHub / Slack / npm tokens,
  credential filenames (`id_rsa`, `.netrc`, `.pgpass`, `credentials`,
  `*.pem`/`*.p12`/`*.pfx`/`*.keystore`) and binary content, with a per-file
  `allow_secrets` override that stays visible in code review.
- Self-extracting POSIX sh bootstrap: private `mktemp -d`, trap-enforced
  cleanup on exit (never defeated by `exec`), a `base64 -d` / `base64 -D` /
  `openssl` decoder ladder, reserved exit codes 95–97, and the host's own
  rc files sourced before the duffel's entry.
- Transports: `ssh` (tty for interactive sessions, pass-through ssh args),
  `docker` / `podman` exec, a generic `exec` prefix for kubectl and friends,
  and a local `sh` test drive — each with `--print` to show the exact argv.
- Argv size budget (default 64 KiB, hard ceiling 100 KiB) with a
  largest-members breakdown on overflow instead of an opaque `E2BIG`.
- `init` starter config, `ls` size/budget report, reproducible `pack`,
  `emit` for piping the bootstrap anywhere.
- 90 deterministic offline tests (`go test ./...`) and an end-to-end
  `scripts/smoke.sh` that prints `SMOKE OK`.

[0.1.0]: https://github.com/JaydenCJ/dotduffel/releases/tag/v0.1.0
