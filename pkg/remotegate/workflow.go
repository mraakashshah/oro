package remotegate

import "errors"

// ErrWorkflowIneligible indicates that a workflow cannot be used for the
// requested remote-gate operation.
//
//oro:testonly — workflow parser consumers are wired by subsequent remote-gate tasks.
var ErrWorkflowIneligible = errors.New("workflow ineligible")
