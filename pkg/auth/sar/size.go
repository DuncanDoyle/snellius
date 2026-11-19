package sar

import "slices"

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

// sizeClasses are the Go allocator's small object sizes; a larger object takes whole pages.
var sizeClasses = [...]int64{
	8, 16, 24, 32, 48, 64, 80, 96, 112, 128, 144, 160, 176, 192, 208, 224, 240, 256,
	288, 320, 352, 384, 416, 448, 480, 512, 576, 640, 704, 768, 896, 1024, 1152, 1280,
	1408, 1536, 1792, 2048, 2304, 2688, 3072, 3200, 3456, 4096, 4864, 5376, 6144, 6528,
	6784, 6912, 8192, 9472, 9728, 10240, 10880, 12288, 13568, 14336, 16384, 18432, 19072,
	20480, 21760, 24576, 27264, 28672, 32768,
}

// pageBytes is the allocator's page, the unit of an object above the largest size class.
const pageBytes = 8192

// size is e's accounted bytes besides its map slot.
func (e *entry) size() int64 { return entryBytes + stringBytes(e.attrs) }

// stringBytes is the heap a string of s's length takes: its allocation's size
// class, or whole pages above the largest (TestLongKeysAccountedAtSizeClass).
func stringBytes(s string) int64 {
	n := int64(len(s))
	if n == 0 {
		return 0
	}
	if i, _ := slices.BinarySearch(sizeClasses[:], n); i < len(sizeClasses) {
		return sizeClasses[i]
	}
	return (n + pageBytes - 1) &^ (pageBytes - 1)
}
