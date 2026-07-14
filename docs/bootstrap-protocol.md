# The bootstrap protocol

This document specifies the self-extracting script that `dotduffel emit`
prints and every transport (`ssh`, `docker`, `podman`, `exec`, `sh`)
carries to the target. The script is the entire deployment mechanism —
there is no agent, no daemon, and nothing to install on either side.
Every clause below is pinned by a test in `internal/bootstrap`.

## Lifecycle contract

1. **Private tempdir.** The script runs `mktemp -d "${TMPDIR:-/tmp}/duffel.XXXXXXXX"`.
   `mktemp -d` creates the directory mode 0700, so other users on a
   shared host cannot read the duffel. If it fails, the script exits 97
   without touching anything else.
2. **Trap-enforced cleanup.** Immediately afterwards it installs
   `trap 'rm -rf "$duffel_dir"' EXIT HUP INT TERM`. This is the
   ephemerality guarantee: whether the session ends with `exit`, a lost
   connection, or a signal, the tempdir goes with it.
3. **No `exec`, ever.** The wrapper starts the user's shell as a child,
   waits, and re-exits with the shell's status. `exec` would replace the
   wrapper and orphan the EXIT trap — the one bug that would leave
   litter on the host — so the generator refuses to produce it and a
   test scans every emitted line for it.
4. **Decode.** The payload is a base64 tar.gz embedded in a single-quoted
   shell word (the base64 alphabet cannot break the quoting). Decoding
   tries `base64 -d` (GNU/busybox), then `base64 -D` (older BSD/macOS),
   then `openssl base64 -d -A`. Failed attempts truncate-overwrite the
   output file, so a partial write from one decoder cannot corrupt the
   next. No decoder at all exits 96.
5. **Extract.** `tar -xzf … -C "$duffel_dir"`, then the archive file is
   removed and the multi-KiB `duffel_b64` variable is unset so it does
   not linger in the session environment. Extraction failure exits 95.
6. **Run.** `DUFFEL_DIR` is exported, then:
   - interactive bash: `bash --rcfile "$duffel_dir/.duffelrc" -i`
   - interactive sh: `ENV="$duffel_dir/.duffelrc" sh -i`
   - command mode: `bash -O expand_aliases -c '<rc-source>\n<command>'`
     (the newline matters: aliases defined while sourcing are only
     visible to lines parsed afterwards).

## The rc wrapper (`.duffelrc`)

Packed into every bundle at the reserved name `.duffelrc`:

1. Sources the host's own defaults first — `/etc/bash.bashrc` and
   `~/.bashrc` (or `~/.shrc` for `shell: "sh"`). The duffel augments the
   box; it never replaces its configuration.
2. Prepends `$DUFFEL_DIR/bin` to `PATH` if the bundle contains a `bin/`.
3. Exports the manifest's `env` map, sorted and shell-quoted.
4. Sources the manifest's `entry` file (default `duffelrc`) — your code.
5. Ends with `true` so optional-file guards cannot leak a failing `$?`
   into the fresh session.

## Reserved exit codes

| Code | Meaning |
| --- | --- |
| 95 | payload extraction failed (`tar` missing or archive damaged) |
| 96 | no base64/openssl decoder available on the target |
| 97 | `mktemp -d` failed |

They sit at the top of the 8-bit range to stay clear of common tool
codes; anything else is the user's shell or command exiting normally.

## Why an argv budget?

The whole script travels as **one argument** to `ssh`/`exec` — that is
what makes dotduffel work with nothing pre-installed on the target. On
Linux a single argv string is capped at `MAX_ARG_STRLEN` (128 KiB);
other systems are less generous. The default budget of 64 KiB (manifest
`budget_kb`, hard ceiling 100) keeps a comfortable margin, and overflow
is reported *before* any connection is made, with a per-file size
breakdown, instead of an opaque `E2BIG` mid-login. Payloads bigger than
the budget are a design smell for a dotfiles bundle — but a stdin-based
transport for large duffels is on the roadmap.

## Target prerequisites

POSIX `sh`, `tar` with gzip support (`-z`), and any of the three
decoders above. That covers stock alpine/busybox, debian/ubuntu,
Fedora, and macOS targets. Nothing is written outside the tempdir.
