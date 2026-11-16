// Package bad exports an analyzer type and a function that accepts one.
package bad

// Analyzer is an exported analyzer type.
type Analyzer func(resource string) []string

// Register accepts an analyzer from outside.
func Register(a func(resource string) []string) { _ = a }
