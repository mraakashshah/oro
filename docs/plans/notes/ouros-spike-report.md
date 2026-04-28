# Ouros Compatibility Spike Report

Bead: `oro-0b0mi`
Date: 2026-04-28
Spec reviewed: `docs/plans/2026-04-28-oro-harness-architecture-spec.md` §8.3-§8.10

## Summary decision

Phase F.0 is **not green for direct Phase F implementation as currently specified**.

Pin `ouros==0.0.7`, but do not implement §8.4 as a direct `cargo install ouros` or upstream binary CLI integration. The actual upstream package is a Python binding distributed as wheels and source, not a standalone CLI matching `oro sandbox run/start/resume/snapshot/fork/list-vars/get-var/kill`.

Recommended distribution decision: **A-modified**: vendor or download the upstream Python wheels for supported Python/platform tags and build an Oro-owned `oro sandbox` shim around the Python API. If requiring a host Python runtime is not acceptable, use §8.10 option **C** until a standalone binary packaging story exists.

## Version pin

- Pinned package: `ouros==0.0.7`
- Upstream source: `https://github.com/parcadei/ouros`
- PyPI metadata: `https://pypi.org/project/ouros/`
- GitHub release: `https://github.com/parcadei/ouros/releases/tag/v0.0.7`
- PyPI summary: "Python bindings for the Ouros sandboxed Python interpreter"
- Requires Python: `>=3.10`
- Local imported version: `ouros.__version__ == "0.0.7"`

Cargo note: `cargo search ouros --limit 5` and `cargo info ouros` identify crates.io `ouros 0.2.0` as unrelated 2D game common functionality. §8.3's `cargo install ouros --version X.Y.Z` path does not target the sandbox package.

## Platform evidence

Local verified platform:

- Host: `Darwin ... RELEASE_ARM64_T6020 arm64`
- Python: `3.13.11`
- Install command: `/Users/as21/.local/bin/python3.13 -m venv ...; pip install --only-binary=:all: ouros==0.0.7`
- Install result: downloaded `ouros-0.0.7-cp313-cp313-macosx_11_0_arm64.whl`; install completed in `real 4.76`
- Native module: `_ouros.cpython-313-darwin.so`
- Native module file evidence: `Mach-O 64-bit dynamically linked shared library arm64`
- Installed native module size: `29,618,656` bytes (`du -h`: `28M`)

Published wheel metadata inspected from PyPI/GitHub release:

| Target | Evidence | Size | SHA256 |
|---|---:|---:|---|
| macOS arm64 CPython 3.13 | `ouros-0.0.7-cp313-cp313-macosx_11_0_arm64.whl` | 12,590,259 bytes | `aef0ea89affcd591ae6299f07f2a8030dba5e5d3dfb50cbd6dabf1094576e15f` |
| Linux x86_64 CPython 3.13 | `ouros-0.0.7-cp313-cp313-manylinux_2_17_x86_64.manylinux2014_x86_64.whl` | 13,878,009 bytes | `c5814cd26453adb296159d4b88c37abf0c24c6f5254d73543b60309ba92a8cd0` |
| Linux arm64 CPython 3.13 | `ouros-0.0.7-cp313-cp313-manylinux_2_17_aarch64.manylinux2014_aarch64.whl` | 12,810,520 bytes | `b713a2e491b048399eec99d9c01d9994a1e613641f9f6195685d774f74735633` |

Limits of verification: I verified execution only on local macOS arm64. Linux platform evidence is metadata/hash inspection of published manylinux wheels, not a live Linux runner.

Source build evidence:

- `/opt/homebrew/bin/python3 -m venv ...; pip install ouros==0.0.7` selected `ouros-0.0.7.tar.gz` and started a local maturin/Rust wheel build instead of using a wheel for that interpreter.
- I stopped that temp-dir source build after it had been compiling for about five minutes; final pip status was a killed build, not a completed source-build verification.
- This supports keeping wheel-tag matching explicit; otherwise installs may silently become Rust source builds.

## API/CLI contract gap

§8.4 expected an `oro sandbox` surface with:

```text
run/start/resume/snapshot/fork/list-vars/get-var/kill
```

Actual upstream install evidence:

- `shutil.which("ouros")` returned `None`.
- The venv `bin/` directory contained only Python/pip activation files, no `ouros` executable.
- `ouros-0.0.7.dist-info` had no `entry_points.txt`.

Actual API surface:

- `ouros.Sandbox(code, inputs=[...], external_functions=[...])`
- `Sandbox.run(inputs=..., limits=..., external_functions=..., print_callback=..., os=...)`
- `Sandbox.start(...) -> Snapshot | FutureSnapshot | Complete`
- `Snapshot.resume(return_value=... | exception=... | future=...)`
- `Sandbox.dump()/Sandbox.load(...)`
- `Snapshot.dump()/Snapshot.load(...)`
- `ouros.SessionManager` / `ouros.Session` for named sessions, persistence, fork, variables, history, heap stats.

§8.4 can still be implemented, but only as an Oro-owned shim. The shim should map:

- `oro sandbox run` -> `Sandbox.run` for one-shot code or `SessionManager.execute` for persistent sessions.
- `oro sandbox start` -> `Sandbox.start` or `SessionManager.execute` when registered external functions may pause.
- `oro sandbox resume` -> `Snapshot.load(...).resume(...)` for serialized snapshots, or `Session.resume(call_id, value)` for named sessions.
- `oro sandbox snapshot` -> `Snapshot.dump()` for paused execution, or `Session.save()` / `SessionManager.save_session()` for session state.
- `oro sandbox fork` -> `Session.fork()` / `SessionManager.fork_session()`.
- `oro sandbox list-vars` -> `Session.list_variables()`.
- `oro sandbox get-var` -> `Session.get_variable(name)`.
- `oro sandbox kill` -> `SessionManager.destroy_session(session_id)`.

Spec amendment needed: replace the `cargo install ouros` path with a Python-wheel/shim path, or explicitly choose constrained fallback C.

## Prototype notes

Local Python binding probes:

- `ouros.Sandbox("2 + 3").run()` returned `5`.
- `ouros.Sandbox("print(2+3)\n2+3").run(print_callback=...)` returned `5` and captured stdout `[("stdout", "5\n")]`.
- `Sandbox.start` paused on an external call and returned `Snapshot(script_name='main.py', function_name='fetch', args=('https://example.com',), kwargs={})`.
- `Snapshot.dump()` produced 257 bytes in the test case; `Snapshot.load(...).resume(return_value="response")` returned `Complete(output='response')`.
- `SessionManager.set_storage_dir(...)`, `Session.save(name="save1")`, and `SessionManager.load_session("save1", session_id="s3")` persisted a session to `save1.bin` and restored variable `x`.
- `Session.fork("s2")` produced independent variable state: source `x == 41`, fork `x == 42` after mutation.

External-function bridge prototype:

- Registered and executed Python callbacks for the five required names:
  - `web_search(query, num_results=5)`
  - `doc_search(library, query)`
  - `read_file_scoped(path)`
  - `glob_files_scoped(pattern)`
  - `llm_query(prompt, model="haiku", max_tokens=500)`
- The sandbox call returned `[1, "Sandbox", "file:docs/spec.md", "README.md", "answer"]`.
- Callback log confirmed default argument propagation for `llm_query`: `("llm_query", "summarize", "haiku", 500)`.

Important limitation: this was a Python-host callback prototype, not Go functions registered directly into ouros. Upstream exposes a Python callback interface; Go-side callbacks require an Oro shim process or embedding approach.

## Security/sandbox observations

Adversarial probes run locally:

- `__import__("os").system("echo BAD")` failed with `SandboxRuntimeError NameError: name '__import__' is not defined`.
- `open("/etc/passwd").read()` failed with `SandboxRuntimeError FileNotFoundError`.
- `while True: pass` with `ResourceLimits(max_duration_secs=0.1)` failed with `SandboxRuntimeError TimeoutError: time limit exceeded`.

Resource limits are available as typed-dict keys:

- `max_allocations`
- `max_duration_secs`
- `max_memory`
- `gc_interval`
- `max_recursion_depth`

Observed risk: filesystem semantics need a dedicated policy layer. `Sandbox.run(..., os=...)` and `ouros.OSAccess` exist, and the default probe blocked `/etc/passwd`, but §8.5's project-tree-only read and `/tmp/oro-sandbox/<session-id>/` write rules must be enforced by the Oro shim, not assumed from the upstream package alone.

## Gate decision

F.0 gate status: **FAILED / DEFERRED pending spec amendment or shim design**.

Gate criteria:

- Pinned ouros version chosen: **met** (`ouros==0.0.7`).
- Working vendoring story exists: **partially met**. Python wheels exist for macOS arm64 and Linux x86_64/aarch64, but there is no standalone upstream binary and the cargo path is unrelated. Requires Oro-owned wheel/shim distribution or fallback C.
- §8.4 CLI surface matches actual API or spec updated: **failed**. Actual upstream package has no CLI; spec was not edited in this report-only spike.
- §8.5 external-function bridge prototype with five named functions: **partially met**. Python callback prototype works; Go callback prototype was not implemented because upstream does not expose a direct Go registration surface.
- §8.9 acceptance tests run against chosen path: **partially met**. Equivalent Python binding probes pass; the literal `oro sandbox ...` commands cannot run until a shim exists.

Phase F should not start against the current §8.3-§8.5 text. Start F.1 only after either:

1. a shim spec bead amends §8.4 and defines the Python-wheel distribution path, or
2. Phase F is descoped to §8.10 option C constrained surface.

## Residual risks

- Upstream package is classified "Development Status :: 3 - Alpha"; API churn is likely.
- Wheel support is CPython-version-specific; unsupported Python versions may fall back to slow or failing source builds.
- Linux wheels were not executed in this spike.
- The current spec's `cargo install ouros` path resolves to the wrong crates.io package.
- A Python runtime dependency may complicate Oro setup if Oro otherwise expects a pure Go/Rust binary distribution.
- A direct Go callback bridge was not proven; shim IPC adds serialization, timeout, lifecycle, and security responsibilities.
- Session auto-expiry after 24 hours is a spec requirement, not observed upstream behavior in this probe.

## Exact commands run

```bash
bd show oro-0b0mi
bd update oro-0b0mi --status in_progress
sed -n '1378,1530p' docs/plans/2026-04-28-oro-harness-architecture-spec.md
rg -n "ouros|Ouros" -S . --glob '!tmp/**' --glob '!node_modules/**' --glob '!vendor/**'
python3 - <<'PY'  # fetched https://pypi.org/pypi/ouros/json and printed release files/hashes
python3 - <<'PY'  # fetched GitHub repo/release/tag metadata via api.github.com/repos/parcadei/ouros
uname -a && python3 -VV && which python3 && rustc --version && cargo --version
which -a python3 python3.13 python3.12 python3.11 | sort -u
cargo search ouros --limit 5
cargo info ouros
/opt/homebrew/bin/python3 -m venv /tmp/oro-0b0mi-ouros.9xOobZ/venv
/tmp/oro-0b0mi-ouros.9xOobZ/venv/bin/python -m pip install --upgrade pip
/usr/bin/time -p /tmp/oro-0b0mi-ouros.9xOobZ/venv/bin/python -m pip install ouros==0.0.7
/Users/as21/.local/bin/python3.13 -m venv /tmp/oro-0b0mi-ouros-wheel.wvpVOO/venv
/tmp/oro-0b0mi-ouros-wheel.wvpVOO/venv/bin/python -m pip install --upgrade pip
/usr/bin/time -p /tmp/oro-0b0mi-ouros-wheel.wvpVOO/venv/bin/python -m pip install --only-binary=:all: ouros==0.0.7
/tmp/oro-0b0mi-ouros-wheel.wvpVOO/venv/bin/python - <<'PY'  # API, version, CLI, method inspection
/tmp/oro-0b0mi-ouros-wheel.wvpVOO/venv/bin/python - <<'PY'  # Sandbox run/start/resume, external fns, escape/resource probes
/tmp/oro-0b0mi-ouros-wheel.wvpVOO/venv/bin/python - <<'PY'  # SessionManager persistence/fork/variable probes
du -h /tmp/oro-0b0mi-ouros-wheel.wvpVOO/venv/lib/python3.13/site-packages/ouros/_ouros.cpython-313-darwin.so
ls -l /tmp/oro-0b0mi-ouros-wheel.wvpVOO/venv/lib/python3.13/site-packages/ouros/_ouros.cpython-313-darwin.so
file /tmp/oro-0b0mi-ouros-wheel.wvpVOO/venv/lib/python3.13/site-packages/ouros/_ouros.cpython-313-darwin.so
find /tmp/oro-0b0mi-ouros-wheel.wvpVOO/venv/lib/python3.13/site-packages -maxdepth 1 -name 'ouros-*.dist-info' ...
find /tmp/oro-0b0mi-ouros-wheel.wvpVOO/venv/bin -maxdepth 1 -type f -or -type l ...
sed -n '1,260p' /tmp/oro-0b0mi-ouros-wheel.wvpVOO/venv/lib/python3.13/site-packages/ouros/__init__.py
sed -n '1,560p' /tmp/oro-0b0mi-ouros-wheel.wvpVOO/venv/lib/python3.13/site-packages/ouros/session.py
sed -n '1,620p' /tmp/oro-0b0mi-ouros-wheel.wvpVOO/venv/lib/python3.13/site-packages/ouros/_ouros.pyi
```
