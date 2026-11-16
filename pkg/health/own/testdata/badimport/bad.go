// Package bad imports the cache, which package own must never reach.
package bad

import _ "github.com/DuncanDoyle/snellius/pkg/cache"
