package sar

// Accounted sizes of the permission cache (D-100): a fixed cost per entry and
// identity plus the lengths of their strings, and a cost per map slot at the
// map's peak. TestAccountedBytesMatchHeap measured 459 B per decision on the heap
// at 10,000 decisions (accounted 475 B); TestRetainedHeapAfterChurn checks churn.
const (
	// entryBytes is one decision or full-read decision: the entry and its LRU element.
	entryBytes = 224
	// identityBytes is one identity's metadata besides its map slot.
	identityBytes = 16
	// decisionSlotBytes and identitySlotBytes are one peak slot of the decision and identity maps.
	decisionSlotBytes = 200
	identitySlotBytes = 72
)

// size is e's accounted bytes besides its map slot.
func (e *entry) size() int64 {
	a := e.key.attrs
	return int64(entryBytes + len(a.Verb) + len(a.Group) + len(a.Resource) + len(a.Namespace) + len(a.Name) + len(e.kinds))
}
