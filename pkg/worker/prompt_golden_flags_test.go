package worker_test

import "flag"

// Accept the shared golden-test flag used by the prompt test lifecycle.
// Prompt goldens in this package are literal assertions, so the flag is inert.
var _ = flag.Bool("update", false, "accepted for prompt golden test compatibility")
