# Phase 10 No-Bd Install Check

Use this check before closing Phase 10 acceptance when the historical
`fresh-mac:latest` Docker image is unavailable.

The original Phase 10 bead referenced:

```bash
docker run --rm -v "$PWD:/oro" fresh-mac /bin/sh -c 'cd /oro && make build && make install && oro task create --type task --title=t --description=d --acceptance-criteria=ac'
```

That image is not part of this repository and may not exist on operator
machines. The replacement in-repo gate is:

```bash
scripts/check-phase10-no-bd-install.sh
```

The script builds a temporary tool PATH that does not resolve `bd`, installs
Oro into temporary `ORO_HOME` and `GOBIN` directories, and exercises the
installed `oro` binary against a temporary native SQLite beadstore:

1. `command -v bd` must fail inside the controlled PATH.
2. `make build` must pass.
3. `make install` must pass.
4. Installed `oro --version` must run.
5. Installed `oro task create`, `show`, `close`, and `show` must work with
   `ORO_BEADSOURCE_MODE=sqlite`.

This proves the Phase 10 property that normal build/install and the native task
lifecycle no longer require the legacy bd CLI. It does not prove that the
operator's machine has no `bd` binary anywhere on disk; it proves Oro does not
resolve or invoke `bd` in the controlled install path.
