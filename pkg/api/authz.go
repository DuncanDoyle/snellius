package api

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

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

// NamedAuthorizer answers named-ref checks (D-100): a detail page's subject,
// or an object named in a readable object. Without it a named ref is checked
// as a population member.
type NamedAuthorizer interface {
	// AuthorizeNamedErr reports whether the caller of r may view the named ref, or why it could not tell.
	AuthorizeNamedErr(r *http.Request, ref objects.Ref) (bool, error)
}

// ListAuthorizer answers population checks (design §7.6, §8.1): whether the
// caller may list a kind in a namespace, "" meaning cluster-wide.
type ListAuthorizer interface {
	// CanList reports whether the caller of r may list gk in namespace.
	CanList(r *http.Request, gk objects.GroupKind, namespace string) (bool, error)
}

// FullReader answers the full-read fast path (design §6.4): whether the
// caller may list every served kind cluster-wide and so sees the allow-all view.
type FullReader interface {
	// FullRead reports whether the caller of r reads everything, or why it could not tell.
	FullRead(r *http.Request) (bool, error)
}

// ListQuery is one population list question: may the caller list Kind in
// Namespace. A cluster-scoped kind is listed cluster-wide whatever Namespace says.
type ListQuery struct {
	Kind      objects.GroupKind
	Namespace string
}

// BatchAuthorizer resolves many population list questions at once, in
// parallel, before a handler renders (D-100 item 6). Each answer is the list
// half of ErrAuthorizer's: a namespaced kind without a namespace is false.
type BatchAuthorizer interface {
	// ResolveLists answers qs in order, or why it could not tell.
	ResolveLists(r *http.Request, qs []ListQuery) ([]bool, error)
}

// ListScanner says whether a list denial is final for a population member
// (D-100). Without it, a denied member is asked again through ErrAuthorizer.
type ListScanner interface {
	// NamedGetScan reports whether a member the list denies is still asked by named get.
	NamedGetScan() bool
}

// Admitter admits requests that need uncached checks a few at a time (D-116).
// The commit wrapper first builds a GET or HEAD without admission; a build that
// meets an uncached check is discarded, and the request waits in Admit and is
// built afresh. Other methods are admitted first. A request holds its
// admission until its response is final, not while it is written.
type Admitter interface {
	// Admit waits under r's context for admission and returns its release, or why it could not admit.
	Admit(r *http.Request) (release func(), err error)
}

// ErrCheckLimit is the error of a request that needs more uncached checks than CheckLimit.
var ErrCheckLimit = errors.New("authorisation check limit reached")

// ErrNotAdmitted is the error of an uncached check made before its request was
// admitted; the commit wrapper admits the request and builds it again (D-116).
var ErrNotAdmitted = errors.New("authorisation: request not admitted")

// Why an uncached check missed the cache, as an authoriser passes it to SpendCheckOn.
const (
	// MissAbsent is a check with no cached answer: never asked, or evicted.
	MissAbsent = "absent"
	// MissAllowExpired and MissDenyExpired are checks whose cached allow or deny expired.
	MissAllowExpired = "allow-expired"
	MissDenyExpired  = "deny-expired"
)

// CheckLimit is the most uncached authorisation checks one request may make (design §6.4, R25).
// The global limiter protects the API server; this bounds one request's spend.
const CheckLimit = 1000

// authzState is one request's authorisation bookkeeping, shared by its goroutines.
type authzState struct {
	mu     sync.Mutex
	checks int
	// passed is set by the check that first passes CheckLimit, so a rebuild never reports it again.
	passed bool
	err    error
	// fullOnce asks the full read at most once, when a population first needs it (D-100).
	fullOnce sync.Once
	// full is its answer; fullFailed, under mu, marks that it recorded the request's error.
	full, fullFailed bool
	// allowExp is the earliest expiry of an allow the request used; zero means none (D-100).
	allowExp time.Time
	// gated refuses uncached checks until the request is admitted; refused marks that one was (D-116).
	gated, refused bool
	// trace hears each counted check, set before the build starts; nil means none.
	trace func(reason string, key any)
}

// notAdmitted reports whether the build refused an uncached check for want of admission.
func (s *authzState) notAdmitted() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.refused
}

// allowExpiry returns the earliest noted allow expiry, or zero.
func (s *authzState) allowExpiry() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.allowExp
}

// spent returns the uncached checks counted so far.
func (s *authzState) spent() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.checks
}

// capPassed reports whether this build made the check that passed CheckLimit.
func (s *authzState) capPassed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.passed
}

// NoteAllow records that r's request used an allow valid until expires; the
// commit wrapper rebuilds a response whose earliest allow expired (D-100).
// Denials are not noted: a stale denial only hides. It is a no-op outside a committed request.
func NoteAllow(r *http.Request, expires time.Time) {
	s := authzStateOf(r)
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.allowExp.IsZero() || expires.Before(s.allowExp) {
		s.allowExp = expires
	}
}

// failed returns the first recorded error, or nil.
func (s *authzState) failed() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

// fullRead reports whether r's request runs under a full read, asking a the
// first time (design §6.4, D-100). An error is recorded and answered false.
func fullRead(r *http.Request, a Authorizer) bool {
	s := authzStateOf(r)
	fr, ok := a.(FullReader)
	if s == nil || !ok {
		return false
	}
	s.fullOnce.Do(func() {
		if authzFailed(r) {
			return
		}
		all, err := fr.FullRead(r)
		if err != nil {
			ReportAuthzError(r, err)
			s.mu.Lock()
			s.fullFailed = true
			s.mu.Unlock()
			return
		}
		s.full = all
	})
	return s.full
}

// fullReadFailed reports whether the full read recorded the request's error.
func (s *authzState) fullReadFailed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.fullFailed
}

// authzFailed reports whether r's request has recorded an authorisation error.
func authzFailed(r *http.Request) bool {
	s := authzStateOf(r)
	return s != nil && s.failed() != nil
}

// decide reports whether the caller of r may view ref, through the
// error-returning form when a has one: an error is recorded and answered as
// deny, and after one every ref of the request is denied without asking.
func decide(r *http.Request, a Authorizer, ref objects.Ref) bool {
	ea, ok := a.(ErrAuthorizer)
	if !ok {
		return a.Authorize(r, ref)
	}
	if authzFailed(r) {
		return false
	}
	allowed, err := ea.AuthorizeErr(r, ref)
	if err != nil {
		ReportAuthzError(r, err)
		return false
	}
	return allowed
}

// decideNamed is decide for a named ref, through NamedAuthorizer when a has it.
func decideNamed(r *http.Request, a Authorizer, ref objects.Ref) bool {
	na, ok := a.(NamedAuthorizer)
	if !ok {
		return decide(r, a, ref)
	}
	if authzFailed(r) {
		return false
	}
	allowed, err := na.AuthorizeNamedErr(r, ref)
	if err != nil {
		ReportAuthzError(r, err)
		return false
	}
	return allowed
}

// decideList reports whether the caller of r may list gk in namespace, as
// decide does; an Authorizer without ListAuthorizer answers false.
func decideList(r *http.Request, a Authorizer, gk objects.GroupKind, namespace string) bool {
	la, ok := a.(ListAuthorizer)
	if !ok || authzFailed(r) {
		return false
	}
	allowed, err := la.CanList(r, gk, namespace)
	if err != nil {
		ReportAuthzError(r, err)
		return false
	}
	return allowed
}

type authzStateKey struct{}

// withAuthzState returns r carrying fresh authorisation bookkeeping, and that bookkeeping.
func withAuthzState(r *http.Request) (*http.Request, *authzState) {
	return withAuthzStateFrom(r, 0, false)
}

// withAuthzStateFrom is withAuthzState with checks already counted, as a
// rebuild keeps them; gated refuses uncached checks until admission.
func withAuthzStateFrom(r *http.Request, checks int, gated bool) (*http.Request, *authzState) {
	s := &authzState{checks: checks, gated: gated}
	return r.WithContext(context.WithValue(r.Context(), authzStateKey{}, s)), s
}

func authzStateOf(r *http.Request) *authzState {
	s, _ := r.Context().Value(authzStateKey{}).(*authzState)
	return s
}

// SpendCheck counts one uncached check for r's request and returns
// ErrCheckLimit past the limit, or ErrNotAdmitted, counting nothing, before
// the request is admitted. It is nil outside a committed request.
func SpendCheck(r *http.Request) error { return SpendCheckOn(r, "", nil) }

// SpendCheckOn is SpendCheck for a check the authoriser names by key, which
// missed for reason (a Miss constant); WithCheckTrace hears each counted one.
func SpendCheckOn(r *http.Request, reason string, key any) error {
	s := authzStateOf(r)
	if s == nil {
		return nil
	}
	s.mu.Lock()
	if s.gated {
		s.refused = true
		s.mu.Unlock()
		return ErrNotAdmitted
	}
	s.checks++
	var err error
	if s.checks > CheckLimit {
		s.passed = s.passed || s.checks == CheckLimit+1
		err = ErrCheckLimit
	}
	s.mu.Unlock()
	if s.trace != nil {
		s.trace(reason, key)
	}
	return err
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

// Narrowed allows only what both Base and Policy allow: secure mode puts a
// build's own authoriser behind SAR this way. The API uses Base's list, batch,
// full-read and admission answers and ANDs Policy into each decision.
type Narrowed struct {
	Base, Policy Authorizer
}

// Authorize implements Authorizer.
func (n Narrowed) Authorize(r *http.Request, ref objects.Ref) bool {
	return n.Base.Authorize(r, ref) && n.Policy.Authorize(r, ref)
}

// unnarrow returns a's base and policy; policy is nil unless a is Narrowed.
func unnarrow(a Authorizer) (base, policy Authorizer) {
	switch n := a.(type) {
	case Narrowed:
		return n.Base, n.Policy
	case *Narrowed:
		return n.Base, n.Policy
	}
	return a, nil
}

// policyFullRead reports whether policy reads everything, as fullRead does
// but asked each time; a policy without FullReader does not.
func policyFullRead(r *http.Request, policy Authorizer) bool {
	fr, ok := policy.(FullReader)
	if !ok || authzFailed(r) {
		return false
	}
	all, err := fr.FullRead(r)
	if err != nil {
		ReportAuthzError(r, err)
		return false
	}
	return all
}

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
