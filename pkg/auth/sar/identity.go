package sar

import (
	"crypto/sha256"
	"encoding/binary"
	"hash"
	"slices"

	"github.com/DuncanDoyle/snellius/pkg/auth/oidc"
)

// idHash identifies an identity in the cache: equal hashes mean the same
// username, UID, groups and extra, whatever their order.
type idHash [sha256.Size]byte

// hashIdentity is SHA-256 over a length-prefixed encoding of username, UID,
// sorted groups and sorted extra, so no two different identities share a hash.
func hashIdentity(id oidc.Identity) idHash {
	h := sha256.New()
	writeString(h, id.Username)
	writeString(h, id.UID)
	writeStrings(h, slices.Sorted(slices.Values(id.Groups)))
	keys := make([]string, 0, len(id.Extra))
	for k := range id.Extra {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	writeLen(h, len(keys))
	for _, k := range keys {
		writeString(h, k)
		writeStrings(h, slices.Sorted(slices.Values(id.Extra[k])))
	}
	var out idHash
	h.Sum(out[:0])
	return out
}

func writeLen(h hash.Hash, n int) {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], uint64(n)) // #nosec G115 -- a length is never negative
	_, _ = h.Write(b[:])
}

func writeString(h hash.Hash, s string) {
	writeLen(h, len(s))
	_, _ = h.Write([]byte(s))
}

func writeStrings(h hash.Hash, ss []string) {
	writeLen(h, len(ss))
	for _, s := range ss {
		writeString(h, s)
	}
}
