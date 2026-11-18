package sar

// Accounted sizes of the permission cache (D-100): a fixed cost per entry and
// identity plus its string's allocation, and a cost per map slot at the map's
// peak. TestAccountedBytesMatchHeap checks them against the heap; TestRetainedHeapAfterChurn checks churn.
const (
	// entryBytes is one decision or full-read decision, its LRU links included.
	entryBytes = 80
	// identityBytes is one identity's metadata besides its map slot.
	identityBytes = 48
	// decisionSlotBytes and identitySlotBytes are one peak slot of the decision and
	// identity maps; a decision slot measured 37–66 B between 1,000 and 57,000 entries.
	decisionSlotBytes = 72
	identitySlotBytes = 72
)

// size is e's accounted bytes besides its map slot.
func (e *entry) size() int64 { return entryBytes + stringBytes(e.attrs) }

// stringBytes is the heap a string of s's length takes: its length rounded up
// to the allocator's size classes, at least to the next 8, 16 or 64 bytes.
func stringBytes(s string) int64 {
	n := int64(len(s))
	switch {
	case n <= 32:
		return (n + 7) &^ 7
	case n <= 256:
		return (n + 15) &^ 15
	default:
		return (n + 63) &^ 63
	}
}
