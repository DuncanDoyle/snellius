// Package bad imports standard-library packages that reach memory, files or the network.
package bad

import (
	_ "net/http"
	_ "os"
	_ "reflect"
	_ "unsafe"
)
