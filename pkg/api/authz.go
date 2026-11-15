package api

import (
	"context"
	"errors"
	"net/http"
	"sync"

	"github.com/DuncanDoyle/snellius/pkg/objects"
)

// Authorizer decides, per request, which objects the caller may see (D-013).
// Findings are filtered by applying it to each finding's resource. Phase 1
// ships AllowAll; per-user authorisation plugs in here later.
type Authorizer interface {
	// Authorize reports whether the caller of r may view the object ref.
	Authorize(r *http.Request, ref objects.Ref) bool
}

// ErrAuthorizer can say that it could not decide (design §6.3). The API then
// answers 503 "authz-unavailable" instead of an empty or partial view.
type ErrAuthorizer interface {
	// AuthorizeErr reports whether the caller of r may view ref, or why it could not tell.
	AuthorizeErr(r *http.Request, ref objects.Ref) (bool, error)
}

// ListAuthorizer answers population checks (design §7.6, §8.1): whether the
// caller may list a kind in a namespace, "" meaning cluster-wide.
type ListAuthorizer interface {
	// CanList reports whether the caller of r may list gk in namespace.
	CanList(r *http.Request, gk objects.GroupKind, namespace string) (bool, error)
}

// ErrCheckLimit is the error of a request that needs more uncached checks than checkLimit.
var ErrCheckLimit = errors.New("authorisation check limit reached")

// checkLimit is the most uncached authorisation checks one request may make (design §6.4).
const checkLimit = 500

// authzState is one request's authorisation bookkeeping, shared by its goroutines.
type authzState struct {
	mu     sync.Mutex
	checks int
	err    error
}

type authzStateKey struct{}

// withAuthzState returns r carrying fresh authorisation bookkeeping, and that bookkeeping.
func withAuthzState(r *http.Request) (*http.Request, *authzState) {
	s := &authzState{}
	return r.WithContext(context.WithValue(r.Context(), authzStateKey{}, s)), s
}

func authzStateOf(r *http.Request) *authzState {
	s, _ := r.Context().Value(authzStateKey{}).(*authzState)
	return s
}

// SpendCheck counts one uncached check for r's request and returns
// ErrCheckLimit past the limit. It is nil outside a committed request.
func SpendCheck(r *http.Request) error {
	s := authzStateOf(r)
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.checks++
	if s.checks > checkLimit {
		return ErrCheckLimit
	}
	return nil
}

// ReportAuthzError records the first authorisation error of r's request. It
// is a no-op outside a committed request.
func ReportAuthzError(r *http.Request, err error) {
	s := authzStateOf(r)
	if s == nil || err == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err == nil {
		s.err = err
	}
}

// AllowAll lets every caller see everything the UI can read: the Phase 1
// trust model, in which whoever reaches the UI may view all it shows.
type AllowAll struct{}

// Authorize implements Authorizer.
func (AllowAll) Authorize(*http.Request, objects.Ref) bool { return true }

// DenyAll denies every ref; secure mode uses it until a real authoriser is wired.
type DenyAll struct{}

// Authorize implements Authorizer.
func (DenyAll) Authorize(*http.Request, objects.Ref) bool { return false }

// AuthorizerFunc adapts a function to Authorizer.
type AuthorizerFunc func(r *http.Request, ref objects.Ref) bool

// Authorize implements Authorizer.
func (f AuthorizerFunc) Authorize(r *http.Request, ref objects.Ref) bool { return f(r, ref) }

// allowsAllHook lets tests mark a counting Authorizer as admitting every
// ref; it is nil in production.
var allowsAllHook func(Authorizer) bool

// allowsAll reports whether a is AllowAll, which the API never calls and
// answers from the precomputed model (design §7.4 rule 10). The check is on
// the exact type: a type embedding AllowAll may override Authorize.
func allowsAll(a Authorizer) bool {
	switch a.(type) {
	case AllowAll, *AllowAll:
		return true
	}
	return allowsAllHook != nil && allowsAllHook(a)
}
