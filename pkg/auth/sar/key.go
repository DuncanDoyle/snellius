package sar

import "encoding/binary"

// decKey names a cached decision: its identity's metadata, unique while the
// identity has entries, and its Attributes encoded by appendAttrs.
type decKey struct {
	id    *idEntry
	attrs string
}

// appendAttrs appends a's fields to b in order, each after its length, so
// different Attributes never encode alike.
func appendAttrs(b []byte, a Attributes) []byte {
	for _, s := range [...]string{a.Verb, a.Group, a.Resource, a.Namespace, a.Name} {
		b = binary.AppendUvarint(b, uint64(len(s)))
		b = append(b, s...)
	}
	return b
}
