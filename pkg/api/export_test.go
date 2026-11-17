package api

import "github.com/DuncanDoyle/snellius/pkg/paths"

// WithPathMemoCap gives each handler its own path memo with a topology cap of n bytes.
func WithPathMemoCap(n int) HandlerOption {
	return func(c *handlerConfig) { c.pathMemo = paths.NewMemoCap(nil, n) }
}
