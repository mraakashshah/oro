#!/usr/bin/env bash
set -euo pipefail
export LC_ALL=C

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

dispatcher_worker="pkg/integration/dispatcher_worker_test.go"
e2e_lifecycle="pkg/integration/e2e_lifecycle_test.go"

if [[ -f "$dispatcher_worker" ]]; then
  perl -0pi -e 's/\n\/\/ --- Mock implementations \(duplicated from dispatcher_test since they'\''re unexported\) ---\n\n.*?func \(m \*mockBeadSource\) Undefer\(_ context\.Context, _ string\) error  \{ return nil \}\n/\n\/\/ Store fakes use beadstore.FakeStore.\n/s' "$dispatcher_worker"
  perl -0pi -e 's/\nfunc \(m \*mockBeadSource\) SetBeads\(beads \[\]protocol\.Bead\) \{\n\tm\.mu\.Lock\(\)\n\tdefer m\.mu\.Unlock\(\)\n\tm\.beads = beads\n\}\n//s' "$dispatcher_worker"
  perl -0pi -e 's/beadSrc := &mockBeadSource\{\}/beadSrc := beadstore.NewFakeStore()/g' "$dispatcher_worker"
  perl -0pi -e 's/^\s*beadSrc\.SetBeads\(nil\)\n//mg' "$dispatcher_worker"
fi

if [[ -f "$e2e_lifecycle" ]]; then
  perl -0pi -e 's/\n\t"sync"\n/\n/' "$e2e_lifecycle"
  perl -0pi -e 's/\n\/\/ trackingBeadSource extends mockBeadSource with Close tracking\.\ntype trackingBeadSource struct \{.*?func \(m \*trackingBeadSource\) Undefer\(_ context\.Context, _ string\) error  \{ return nil \}\n//s' "$e2e_lifecycle"
  perl -0pi -e 's/\nfunc \(m \*trackingBeadSource\) SetBeads\(beads \[\]protocol\.Bead\) \{\n\tm\.mu\.Lock\(\)\n\tdefer m\.mu\.Unlock\(\)\n\tm\.beads = beads\n\}\n\nfunc \(m \*trackingBeadSource\) ClosedBeads\(\) \[\]string \{\n\tm\.closeMu\.Lock\(\)\n\tdefer m\.closeMu\.Unlock\(\)\n\tdst := make\(\[\]string, len\(m\.closed\)\)\n\tcopy\(dst, m\.closed\)\n\treturn dst\n\}\n//s' "$e2e_lifecycle"
  perl -0pi -e 's/beadSrc := &trackingBeadSource\{\}/beadSrc := beadstore.NewFakeStore()/g' "$e2e_lifecycle"
  perl -0pi -e 's/^\s*beadSrc\.SetBeads\(nil\).*?\n//mg' "$e2e_lifecycle"
fi

gofmt -w pkg/beadstore/testfake.go "$dispatcher_worker" "$e2e_lifecycle"

cat <<'REPORT'
codemod-fakerunner: transformed mechanical Store mocks:
  - pkg/integration/dispatcher_worker_test.go: mockBeadSource -> beadstore.FakeStore
  - pkg/integration/e2e_lifecycle_test.go: trackingBeadSource -> beadstore.FakeStore

codemod-fakerunner: residue intentionally left for oro-d0ep:
  - pkg/dispatcher/beadsource_test.go mockCommandRunner CLI parsing/command assertion tests
  - cmd/oro/cmd_work_execute_test.go mockBeadSource tests with per-call error/update/close state
  - pkg/dispatcher/dispatcher_test.go mockBeadSource tests with extensive error injection and call tracking
REPORT
