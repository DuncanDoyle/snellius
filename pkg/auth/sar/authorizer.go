// Package sar authorises each signed-in user's view by asking the API server
// SubjectAccessReviews, through a per-identity cache (design §6, D-091, D-100).
package sar

import (
	"cmp"
	"context"
	"errors"
	"net/http"
	"time"

	"k8s.io/utils/clock"

	"github.com/DuncanDoyle/snellius/pkg/api"
	"github.com/DuncanDoyle/snellius/pkg/auth/oidc"
	"github.com/DuncanDoyle/snellius/pkg/auth/session"
	"github.com/DuncanDoyle/snellius/pkg/objects"
	"github.com/DuncanDoyle/snellius/pkg/obs"
)

// Kind is the resource a group and kind map to.
type Kind struct {
	Group      string
	Resource   string
	Namespaced bool
}

// Kinds maps the kinds Snellius serves to their resources.
type Kinds interface {
	// Lookup returns the resource of group and kind; false means unmapped.
	Lookup(group, kind string) (Kind, bool)
	// All returns every mapped kind and a key that changes when the set does.
	All() (kinds []Kind, key string)
}

// Options configure an Authorizer.
type Options struct {
	Checker Checker
	Kinds   Kinds
	// AllowTTL bounds an allow from the start of its SAR; zero means DefaultAllowTTL.
	AllowTTL time.Duration
	Clock    clock.WithTicker
	// Metrics receives the authz series; nil keeps them unexported.
	Metrics *obs.Registry
	// IdentityFrom returns the signed-in identity; nil means session.IdentityFrom.
	IdentityFrom func(context.Context) (oidc.Identity, bool)
	// NamedGetScan adds a named get to population checks the list denies (D-100).
	NamedGetScan bool
	// MaxBytes bounds the permission cache, decisions and full reads together; zero means DefaultMaxBytes.
	MaxBytes int64
	// MaxInFlight, Rate and Burst bound the SARs sent across requests: at once,
	// per second, and at once above the rate (D-100). Zero means the Default value.
	MaxInFlight, Rate, Burst int
}

// Decision lifetimes from the start of the SAR (design §6.2, D-100).
const (
	// DefaultAllowTTL is an allow's lifetime when Options.AllowTTL is zero.
	DefaultAllowTTL = 60 * time.Second
	// DenyTTL is the longest a deny lives: a stale deny only hides.
	DenyTTL = 5 * time.Minute
	// MinDenyTTL is the shortest: a jittered deny spreads refreshes, a shorter one only asks again sooner.
	MinDenyTTL = 4 * time.Minute
)

// sweepEvery is how often Run drops expired decisions.
const sweepEvery = time.Minute

// ErrNoIdentity is the error of a check on a request without a signed-in identity.
var ErrNoIdentity = errors.New("authorisation: no signed-in identity")

// Authorizer decides visibility per signed-in user (design §6.1, D-100):
// populations by list on the resource in the namespace, named refs by that
// list, then get on the named object.
type Authorizer struct {
	kinds        Kinds
	namedGetScan bool
	identityFrom func(context.Context) (oidc.Identity, bool)
	clock        clock.WithTicker
	cache        *cache
	full         *fullCache
	// admission admits the requests that need uncached SARs (D-116).
	admission *admission
	// parallel bounds the SARs of one full-read evaluation; tests lower it.
	parallel int
	// spend, report and note are api.SpendCheckOn, api.ReportAuthzError and api.NoteAllow; tests swap them.
	spend  func(r *http.Request, reason string, key any) error
	report func(*http.Request, error)
	note   func(*http.Request, time.Time)
	// Test hooks, nil in production: run after each sweep; a full-read evaluation stopped early.
	swept, fullStopped func()
}

var (
	_ api.Authorizer      = (*Authorizer)(nil)
	_ api.ErrAuthorizer   = (*Authorizer)(nil)
	_ api.NamedAuthorizer = (*Authorizer)(nil)
	_ api.ListAuthorizer  = (*Authorizer)(nil)
	_ api.FullReader      = (*Authorizer)(nil)
	_ api.BatchAuthorizer = (*Authorizer)(nil)
	_ api.ListScanner     = (*Authorizer)(nil)
	_ api.Admitter        = (*Authorizer)(nil)
)

// New returns an Authorizer; call Run to sweep its cache.
func New(o Options) *Authorizer {
	if o.AllowTTL == 0 {
		o.AllowTTL = DefaultAllowTTL
	}
	if o.Clock == nil {
		o.Clock = clock.RealClock{}
	}
	if o.IdentityFrom == nil {
		o.IdentityFrom = session.IdentityFrom
	}
	if o.MaxBytes == 0 {
		o.MaxBytes = DefaultMaxBytes
	}
	o.MaxInFlight = cmp.Or(o.MaxInFlight, DefaultMaxInFlight)
	o.Rate = cmp.Or(o.Rate, DefaultRate)
	o.Burst = cmp.Or(o.Burst, DefaultBurst)
	lim := newLimiter(o.Clock, o.MaxInFlight, o.Rate, o.Burst)
	c := newCache(o.Checker, lim, o.Clock, o.AllowTTL, o.MaxBytes, newMetrics(o.Metrics))
	// Admitted requests, batchParallel SARs each, can fill every permit (D-116).
	places := o.MaxInFlight / batchParallel
	return &Authorizer{
		kinds:        o.Kinds,
		namedGetScan: o.NamedGetScan,
		identityFrom: o.IdentityFrom,
		clock:        o.Clock,
		cache:        c,
		full:         &fullCache{c: c},
		admission:    newAdmission(places),
		parallel:     fullReadParallel,
		spend:        api.SpendCheckOn,
		report:       api.ReportAuthzError,
		note:         api.NoteAllow,
	}
}

// Admit implements api.Admitter: it waits under r's context for one of the
// MaxInFlight / 8 admission places, in arrival order (D-116). Too many
// requests waiting, in all or of r's identity, is ErrOverloaded. A request
// without an identity can send no SAR, so it is admitted without a place.
func (a *Authorizer) Admit(r *http.Request) (func(), error) {
	id, ok := a.identityFrom(r.Context())
	if !ok {
		return func() {}, nil
	}
	return a.admission.admit(r.Context(), hashIdentity(id))
}

// Run sweeps expired decisions every minute until ctx ends.
func (a *Authorizer) Run(ctx context.Context) {
	t := a.clock.NewTicker(sweepEvery)
	defer t.Stop()
	a.run(ctx, t.C())
}

func (a *Authorizer) run(ctx context.Context, tick <-chan time.Time) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick:
			now := a.clock.Now()
			a.cache.sweep(now)
			if a.swept != nil {
				a.swept()
			}
		}
	}
}

// AuthorizeErr implements api.ErrAuthorizer as the population check: list
// on ref's resource in its namespace, and a named get only with NamedGetScan.
// An unmapped kind, or a namespaced kind without a namespace, is denied without a SAR.
func (a *Authorizer) AuthorizeErr(r *http.Request, ref objects.Ref) (bool, error) {
	return a.authorize(r, ref, a.namedGetScan)
}

// AuthorizeNamedErr implements api.NamedAuthorizer: list, then get on the
// named object, which honours resourceNames grants.
func (a *Authorizer) AuthorizeNamedErr(r *http.Request, ref objects.Ref) (bool, error) {
	return a.authorize(r, ref, true)
}

func (a *Authorizer) authorize(r *http.Request, ref objects.Ref, named bool) (bool, error) {
	id, ok := a.identityFrom(r.Context())
	if !ok {
		return false, ErrNoIdentity
	}
	list, ok := a.listAttrs(ref.Group, ref.Kind, ref.Namespace)
	if !ok || len(ref.Name) > maxNameLen {
		return false, nil
	}
	h := hashIdentity(id)
	d, err := a.check(r, id, h, list)
	if err != nil || d.allowed || !named {
		return a.allow(r, d, err)
	}
	get := list
	get.Verb, get.Name = "get", ref.Name
	d, err = a.check(r, id, h, get)
	return a.allow(r, d, err)
}

// Kubernetes' longest object name and namespace; a longer one names no object
// and is denied without a SAR, so it never reaches the cache.
const (
	maxNameLen      = 253
	maxNamespaceLen = 63
)

// listAttrs is the population list check of a group, kind and namespace:
// cluster-wide for a cluster-scoped kind. False for an unmapped kind or a
// namespaced kind without a valid-length namespace, which are denied without a SAR.
func (a *Authorizer) listAttrs(group, kind, namespace string) (Attributes, bool) {
	k, ok := a.kinds.Lookup(group, kind)
	if !ok {
		return Attributes{}, false
	}
	ns := ""
	if k.Namespaced {
		if namespace == "" || len(namespace) > maxNamespaceLen {
			return Attributes{}, false
		}
		ns = namespace
	}
	return Attributes{Verb: "list", Group: k.Group, Resource: k.Resource, Namespace: ns}, true
}

// Authorize implements api.Authorizer with population semantics: an error is
// reported for the request and answered as deny.
func (a *Authorizer) Authorize(r *http.Request, ref objects.Ref) bool {
	ok, err := a.AuthorizeErr(r, ref)
	if err != nil {
		a.report(r, err)
		return false
	}
	return ok
}

// CanList implements api.ListAuthorizer. It asks only list, never a named
// get; an unmapped kind or an overlong namespace is false.
func (a *Authorizer) CanList(r *http.Request, gk objects.GroupKind, namespace string) (bool, error) {
	id, ok := a.identityFrom(r.Context())
	if !ok {
		return false, ErrNoIdentity
	}
	k, ok := a.kinds.Lookup(gk.Group, gk.Kind)
	if !ok || k.Namespaced && len(namespace) > maxNamespaceLen {
		return false, nil
	}
	if !k.Namespaced {
		namespace = ""
	}
	d, err := a.check(r, id, hashIdentity(id), Attributes{Verb: "list", Group: k.Group, Resource: k.Resource, Namespace: namespace})
	return a.allow(r, d, err)
}

func (a *Authorizer) check(r *http.Request, id oidc.Identity, h idHash, at Attributes) (decision, error) {
	return a.cache.check(r.Context(), func(reason string) error { return a.spend(r, reason, at) }, id, h, at)
}

// allow reports d's answer to r's request, noting an allow's expiry (D-100).
func (a *Authorizer) allow(r *http.Request, d decision, err error) (bool, error) {
	if err == nil && d.allowed {
		a.note(r, d.expires)
	}
	return d.allowed, err
}
