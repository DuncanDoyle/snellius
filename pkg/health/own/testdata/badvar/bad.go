// Package bad holds package state, which package own must never declare.
package bad

var seen = map[string]bool{}

// Seen reads the state.
func Seen(k string) bool { return seen[k] }
