// Package sar authorises each signed-in user's view by asking the API server
// SubjectAccessReviews, through a per-identity cache (design §6, D-091, D-099).
package sar

import (
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
	// TTL bounds every decision from the start of its SAR; zero means DefaultTTL.
	TTL   time.Duration
	Clock clock.WithTicker
	// Metrics receives the authz series; nil keeps them unexported.
	Metrics *obs.Registry
	// IdentityFrom returns the signed-in identity; nil means session.IdentityFrom.
	IdentityFrom func(context.Context) (oidc.Identity, bool)
}

// DefaultTTL is the decision lifetime when Options.TTL is zero (design §6.2).
const DefaultTTL = 30 * time.Second

// sweepEvery is how often Run drops expired decisions.
const sweepEvery = time.Minute

// ErrNoIdentity is the error of a check on a request without a signed-in identity.
var ErrNoIdentity = errors.New("authorisation: no signed-in identity")

// Authorizer decides visibility per signed-in user: list on the resource in
// the namespace, then get on the named object (design §6.1).
type Authorizer struct {
	kinds        Kinds
	identityFrom func(context.Context) (oidc.Identity, bool)
	clock        clock.WithTicker
	cache        *cache
	// spend and report are api.SpendCheck and api.ReportAuthzError; tests swap them.
	spend  func(*http.Request) error
	report func(*http.Request, error)
	// swept is a test hook run after each sweep; nil in production.
	swept func()
}

var (
	_ api.Authorizer     = (*Authorizer)(nil)
	_ api.ErrAuthorizer  = (*Authorizer)(nil)
	_ api.ListAuthorizer = (*Authorizer)(nil)
)

// New returns an Authorizer; call Run to sweep its cache.
func New(o Options) *Authorizer {
	if o.TTL == 0 {
		o.TTL = DefaultTTL
	}
	if o.Clock == nil {
		o.Clock = clock.RealClock{}
	}
	if o.IdentityFrom == nil {
		o.IdentityFrom = session.IdentityFrom
	}
	return &Authorizer{
		kinds:        o.Kinds,
		identityFrom: o.IdentityFrom,
		clock:        o.Clock,
		cache:        newCache(o.Checker, o.Clock, o.TTL, newMetrics(o.Metrics)),
		spend:        api.SpendCheck,
		report:       api.ReportAuthzError,
	}
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
			a.cache.sweep(a.clock.Now())
			if a.swept != nil {
				a.swept()
			}
		}
	}
}

// AuthorizeErr implements api.ErrAuthorizer. An unmapped kind, or a
// namespaced kind without a namespace, is denied without a SAR.
func (a *Authorizer) AuthorizeErr(r *http.Request, ref objects.Ref) (bool, error) {
	id, ok := a.identityFrom(r.Context())
	if !ok {
		return false, ErrNoIdentity
	}
	k, ok := a.kinds.Lookup(ref.Group, ref.Kind)
	if !ok {
		return false, nil
	}
	ns := ""
	if k.Namespaced {
		if ref.Namespace == "" {
			return false, nil
		}
		ns = ref.Namespace
	}
	h := hashIdentity(id)
	list := Attributes{Verb: "list", Group: k.Group, Resource: k.Resource, Namespace: ns}
	d, err := a.check(r, id, h, list)
	if err != nil || d.allowed {
		return d.allowed, err
	}
	get := list
	get.Verb, get.Name = "get", ref.Name
	d, err = a.check(r, id, h, get)
	return d.allowed, err
}

// Authorize implements api.Authorizer: an error is reported for the request and answered as deny.
func (a *Authorizer) Authorize(r *http.Request, ref objects.Ref) bool {
	ok, err := a.AuthorizeErr(r, ref)
	if err != nil {
		a.report(r, err)
		return false
	}
	return ok
}

// CanList implements api.ListAuthorizer. It asks only list, never a named
// get; an unmapped kind is false.
func (a *Authorizer) CanList(r *http.Request, gk objects.GroupKind, namespace string) (bool, error) {
	id, ok := a.identityFrom(r.Context())
	if !ok {
		return false, ErrNoIdentity
	}
	k, ok := a.kinds.Lookup(gk.Group, gk.Kind)
	if !ok {
		return false, nil
	}
	if !k.Namespaced {
		namespace = ""
	}
	d, err := a.check(r, id, hashIdentity(id), Attributes{Verb: "list", Group: k.Group, Resource: k.Resource, Namespace: namespace})
	return d.allowed, err
}

func (a *Authorizer) check(r *http.Request, id oidc.Identity, h idHash, at Attributes) (decision, error) {
	return a.cache.check(r.Context(), func() error { return a.spend(r) }, id, h, at)
}
