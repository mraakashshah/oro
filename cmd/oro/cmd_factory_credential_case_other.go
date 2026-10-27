//go:build !darwin

package main

import "os"

// Other platforms fail closed when prospective case equivalence is unknown.
func factoryCredentialCaseSensitive(_ *os.File) (sensitive, known bool) { return false, false }
