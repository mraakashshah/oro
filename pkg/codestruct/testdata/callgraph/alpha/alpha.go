package alpha

import "fmt"

// Alpha calls Beta (same-package, in-project) and fmt.Println (external, unresolved).
func Alpha() {
	Beta()
	fmt.Println("hello")
}

// Beta is a same-package callee.
func Beta() {}
