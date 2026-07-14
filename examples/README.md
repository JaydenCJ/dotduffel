# dotduffel examples

A complete, runnable duffel: manifest, entry file, aliases and a tool
that rides in `bin/`. Everything runs offline.

## 1. Test-drive the duffel locally

The `sh` command runs the exact same lifecycle as an ssh session —
tempdir, extraction, rc wrapper, cleanup — just without a network:

```bash
cd examples
dotduffel --manifest duffel.json sh
```

You land in a shell with a `(duffel) ` prompt, `ll`/`gs` aliases,
`EDITOR=vi`, and `portcheck.sh` on PATH. Type `exit` and check that
`$DUFFEL_DIR` is gone.

One-off commands work too (aliases included):

```bash
dotduffel --manifest duffel.json sh --command 'll "$DUFFEL_DIR"'
```

## 2. See exactly what would run remotely

No transport executes anything you have not seen — `--print` shows the
final argv, payload and all:

```bash
dotduffel --manifest duffel.json ssh --print devbox | head -c 300
dotduffel --manifest duffel.json docker --print mybox | head -c 300
```

## 3. Inspect the bundle itself

`pack` writes the raw archive; it is a plain tar.gz and byte-identical
on every run:

```bash
dotduffel --manifest duffel.json pack -o /tmp/duffel.tgz
tar -tzf /tmp/duffel.tgz
dotduffel --manifest duffel.json pack -o /tmp/again.tgz
cmp /tmp/duffel.tgz /tmp/again.tgz && echo reproducible
```

## 4. Trip the secret guard on purpose

```bash
printf -- '-----BEGIN RSA PRIVATE KEY-----\nAAAA\n' > oops.pem
printf '%s\n' '{ "files": [ { "from": "duffelrc" }, { "from": "oops.pem" } ] }' > leaky.json
dotduffel --manifest leaky.json emit; echo "exit=$?"   # exit=1, names file and rule
rm oops.pem leaky.json
```

The refusal message shows the per-file `allow_secrets` override — the
escape hatch is explicit and reviewable, never a global flag.
