package main

import (
	"fixture/pkg/caller"
	"fixture/pkg/dispatcher"
)

// main is the fixture entry point.
func main() {
	d := &dispatcher.Dispatcher{}
	caller.StartAll(d)
}
