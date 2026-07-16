//go:build linux

package processenv

import (
	"fmt"
	"os"
)

// ReadEntries returns the process environment as its original NUL-delimited
// entries. Callers must fail closed when this cannot be read.
func ReadEntries(pid int) ([]string, error) {
	contents, err := os.ReadFile(fmt.Sprintf("/proc/%d/environ", pid))
	if err != nil {
		return nil, err
	}
	return splitNULDelimitedEntries(contents), nil
}
