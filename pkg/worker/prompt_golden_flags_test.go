package worker_test

import "flag"

// updateGolden accepts the shared golden-test flag used by the prompt test
// lifecycle. Prompt goldens in this package are literal assertions, so the flag
// is intentionally inert.
var updateGolden = flag.Bool("update", false, "accepted for prompt golden test compatibility")
