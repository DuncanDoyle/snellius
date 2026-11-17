package explore

import (
	"slices"

	"github.com/DuncanDoyle/snellius/pkg/computed"
	"github.com/DuncanDoyle/snellius/pkg/objects"
)

// Chains is views B and C as a viewer sees them who reads only some
// delegation chains (secure mode, D-113). A chain is readable when visible
// admits its root's view A deps and every route on it. Rows, contexts,
// effective edges and the dropped flag come from readable chains only, so
// a hidden chain's existence or spec never adds or removes anything. Shared
// compute bounds stay install-wide (D-112). One request uses a Chains.
type Chains struct {
	ix      *Index
	visible func(objects.Ref) bool
	// list reports whether the viewer may list a kind in a namespace, "" meaning cluster-wide.
	list func(objects.GroupKind, string) bool
	// reached and incomplete memoise per route: 1 yes, -1 no, 2 being decided.
	reached, incomplete map[objects.Ref]int8
	rows                map[int]*chainRow
	selections          map[RefKey]bool
	edges               map[objects.Ref][]*computed.EffectiveEdge
}

// chainRow is a row as Chains sees it; ok is false when no readable chain reaches its parent.
type chainRow struct {
	row DelegRow
	ok  bool
}

// Chains returns the readable-chain view of ix for visible and list, which are never nil.
func (ix *Index) Chains(visible func(objects.Ref) bool, list func(objects.GroupKind, string) bool) *Chains {
	return &Chains{ix: ix, visible: visible, list: list, reached: map[objects.Ref]int8{}, incomplete: map[objects.Ref]int8{},
		rows: map[int]*chainRow{}, selections: map[RefKey]bool{}, edges: map[objects.Ref][]*computed.EffectiveEdge{}}
}

// Reached reports whether a readable chain reaches route in the reach graph,
// which ignores fit: route is a readable root, or a readable reach parent is reached.
func (c *Chains) Reached(route objects.Ref) bool {
	return c.walk(c.reached, route, func(r objects.Ref) bool {
		return slices.ContainsFunc(c.ix.rootDeps[r], func(d DepSet) bool { return c.ix.Visible(d, c.visible) })
	})
}

// incompleteAt reports whether route is in a reach closure over readable
// chains: the edge cap hit, or a readable uncertain point reaches it (rules §8.5).
func (c *Chains) incompleteAt(route objects.Ref) bool {
	return c.ix.closureAll || c.walk(c.incomplete, route, func(r objects.Ref) bool { return c.ix.uncertain[r] })
}

// walk reports whether route is visible and seed holds for it or a visible
// reach ancestor, memoised in memo. A no that met a route still being
// decided, on a cycle, is not memoised: another way in may still reach it.
func (c *Chains) walk(memo map[objects.Ref]int8, route objects.Ref, seed func(objects.Ref) bool) bool {
	ok, _ := c.walkFrom(memo, route, seed)
	return ok
}

// walkFrom is walk, and whether the answer met a route still being decided.
func (c *Chains) walkFrom(memo map[objects.Ref]int8, route objects.Ref, seed func(objects.Ref) bool) (ok, open bool) {
	switch memo[route] {
	case 1:
		return true, false
	case -1:
		return false, false
	case 2:
		return false, true
	}
	if !c.visible(route) {
		memo[route] = -1
		return false, false
	}
	if seed(route) {
		memo[route] = 1
		return true, false
	}
	memo[route] = 2
	for _, p := range c.ix.reachParents[route] {
		pok, popen := c.walkFrom(memo, p, seed)
		if pok {
			memo[route] = 1
			return true, false
		}
		open = open || popen
	}
	memo[route] = -1
	if open {
		memo[route] = 0
	}
	return false, open
}

// SelectionHidden reports whether ref k's selection is unreadable, or an
// earlier ref's of the same rule, whose children the controller hands on
// to the later refs (rules §5.2, quirk 1). Permissions alone decide it,
// never which routes exist.
func (c *Chains) SelectionHidden(k RefKey) bool {
	if h, ok := c.selections[k]; ok {
		return h
	}
	h := false
	for j := 0; j <= k.Ref && !h; j++ {
		if s, ok := c.ix.Selection(RefKey{k.Parent, k.Rule, j}); ok {
			h = !c.readable(s)
		}
	}
	c.selections[k] = h
	return h
}

// readable reports whether the viewer may read all of s: its named target,
// or a list of HTTPRoutes in its namespace, or cluster-wide with Namespaces too
// when labels pick them (D-110).
func (c *Chains) readable(s Selection) bool {
	routes := objects.GroupKind{Group: objects.GatewayAPIGroup, Kind: "HTTPRoute"}
	switch {
	case s.Named:
		return c.visible(s.Target)
	case s.All:
		return c.list(routes, "") && (!s.ByLabels || c.list(objects.GroupKind{Kind: "Namespace"}, ""))
	}
	return c.list(routes, s.Namespace)
}

// Row is DelegRows[i] as the viewer sees it, and false when no readable
// chain reaches its parent or its ref's selection is hidden. Its contexts are the readable retained ones and
// its outcome is theirs (rules §5.5); a row whose contexts were cut by a
// compute bound is unknown too-large. It has no child-entry check, which
// compares every chain with the controller, and its Deps are empty.
func (c *Chains) Row(i int) (*DelegRow, bool) {
	if cr := c.rows[i]; cr != nil {
		return &cr.row, cr.ok
	}
	src := &c.ix.DelegRows[i]
	cr := &chainRow{ok: c.Reached(src.Key.Parent) && !c.SelectionHidden(RefKey{src.Key.Parent, src.Key.Rule, src.Key.Ref})}
	c.rows[i] = cr
	if !cr.ok {
		return nil, false
	}
	r := DelegRow{Row: src.Row}
	r.Contexts, r.Reporters, r.KeyDigests, r.KeyDeps, r.Reason, r.Unconfirmable = nil, nil, nil, nil, nil, false
	var keep []int
	for j := range src.Contexts {
		if c.ix.Visible(src.FullContextDeps[j], c.visible) {
			keep = append(keep, j)
		}
	}
	for _, j := range keep {
		ctx := &src.Contexts[j]
		r.Contexts = append(r.Contexts, *ctx)
		r.ContextDeps, r.FullContextDeps = append(r.ContextDeps, src.ContextDeps[j]), append(r.FullContextDeps, src.FullContextDeps[j])
		if k, ok := slices.BinarySearchFunc(r.Reporters, ctx.Reporter, computed.ReportKey.Compare); !ok {
			r.Reporters = slices.Insert(r.Reporters, k, ctx.Reporter)
		}
		if r.Reason == nil {
			r.Reason = ctx.Reason
		}
	}
	if src.Unknown != computed.UnknownScope {
		c.aggregate(&r)
	}
	cr.row = r
	return &cr.row, true
}

// aggregate sets r's outcome from its meta and readable contexts, as the
// rule sets' accumulators do (rules §5.5), with readable reach closures.
func (c *Chains) aggregate(r *DelegRow) {
	count := func(o computed.RowOutcome) int {
		return len(slices.DeleteFunc(slices.Clone(r.Contexts), func(x computed.Context) bool { return outcomeOf(x) != o }))
	}
	unknown := computed.UnknownReason("")
	for _, x := range r.Contexts {
		if outcomeOf(x) == computed.OutcomeUnknown && unknown == "" {
			unknown = x.Unknown
		}
	}
	n := len(r.Contexts)
	r.Outcome, r.Unknown, r.SomeNotComputed = "", "", false
	switch {
	case r.ContextsTruncated:
		r.Outcome, r.Unknown = computed.OutcomeUnknown, computed.UnknownTooLarge
	case r.Allows == computed.AllowsOther || count(computed.OutcomeForbidden) > 0:
		r.Outcome = computed.OutcomeForbidden
	case r.Key.Child == (objects.Ref{}) && r.Unresolved == "":
		r.Outcome, r.Unknown = computed.OutcomeUnknown, computed.UnknownNotReached
		if n > 0 {
			r.Unknown = unknown
		}
	case r.Key.Child == (objects.Ref{}):
		r.Outcome = computed.OutcomeUnresolved
	case n == 0:
		r.Outcome, r.Unknown = computed.OutcomeUnknown, computed.UnknownNotReached
		if c.ix.DelegationCap != "" {
			r.Unknown = computed.UnknownTooLarge
		}
	default:
		missing := count(computed.OutcomeUnknown) > 0 || c.incompleteAt(r.Key.Child)
		att := count(computed.OutcomeAttached)
		switch {
		case att > 0:
			r.Outcome, r.SomeNotComputed = computed.OutcomeAttached, missing
			if att+count(computed.OutcomeUnknown) < n {
				r.Outcome = computed.OutcomePartial
			}
		case count(computed.OutcomeRejectedHostnames) > 0 && missing:
			r.Outcome, r.SomeNotComputed = computed.OutcomeRejectedHostnames, true
		case missing:
			r.Outcome, r.Unknown = computed.OutcomeUnknown, unknown
			if r.Unknown == "" {
				r.Unknown = computed.UnknownContext
			}
		default:
			for _, o := range []computed.RowOutcome{computed.OutcomeRejectedHostnames, computed.OutcomeCycle,
				computed.OutcomeUnresolved, computed.OutcomeDropped} {
				if count(o) > 0 {
					r.Outcome = o
					break
				}
			}
		}
	}
	r.Deps, r.Check = nil, nil
}

// outcomeOf is x's outcome; any value the rows do not count counts as unknown.
func outcomeOf(x computed.Context) computed.RowOutcome {
	switch x.Outcome {
	case computed.OutcomeAttached, computed.OutcomeDropped, computed.OutcomeForbidden, computed.OutcomeRejectedHostnames,
		computed.OutcomeCycle, computed.OutcomeUnresolved:
		return x.Outcome
	}
	return computed.OutcomeUnknown
}

// Dropped reports whether a row the viewer sees, of an owned parent, drops,
// forbids or rejects child: its dropped flag over readable chains.
func (c *Chains) Dropped(child objects.Ref) bool {
	return slices.ContainsFunc(c.ix.flaggers[child], func(i int) bool {
		r, ok := c.Row(i)
		return ok && (r.Outcome == computed.OutcomeDropped || r.Outcome == computed.OutcomeForbidden || r.Outcome == computed.OutcomeRejectedHostnames)
	})
}

// Edges are child's view C edges as the viewer sees them, index-aligned with
// its entry's Edges: nil for an edge with no readable retained context, else
// a copy with only those contexts.
func (c *Chains) Edges(child objects.Ref) []*computed.EffectiveEdge {
	if es, ok := c.edges[child]; ok {
		return es
	}
	e := c.ix.Effective[child]
	out := make([]*computed.EffectiveEdge, len(e.Edges))
	for j := range e.Edges {
		ed := e.Edges[j]
		ed.Contexts, ed.Deps = nil, nil
		for x := range e.Edges[j].Contexts {
			if c.ix.Visible(e.FullContextDeps[j][x], c.visible) {
				ed.Contexts = append(ed.Contexts, e.Edges[j].Contexts[x])
			}
		}
		if len(ed.Contexts) > 0 {
			out[j] = &ed
		}
	}
	c.edges[child] = out
	return out
}

// Prefetch adds the refs that the flag of each of children reads, for one prefetch round.
func (c *Chains) Prefetch(children []objects.Ref, add func(objects.Ref)) {
	for _, ch := range children {
		for _, i := range c.ix.flaggers[ch] {
			r := &c.ix.DelegRows[i]
			add(r.Key.Parent)
			for _, d := range r.FullContextDeps {
				for _, k := range d {
					add(c.ix.Refs[k])
				}
			}
		}
	}
}
