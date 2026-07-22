package remotegate

import "errors"

// ErrWorkflowIneligible indicates that a workflow cannot be used for a remote
// workflow capability preflight.
var ErrWorkflowIneligible = errors.New("workflow ineligible")
