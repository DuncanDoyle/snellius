// Package health holds a type whose short name matches one in the health
// package's tests, so deepPrint's type identity can be tested.
package health

// Twin has the same short name and fields as the health tests' Twin.
type Twin struct{ A int }
