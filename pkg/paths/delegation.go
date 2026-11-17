package paths

import (
	"cmp"
	"math"
	"slices"

	"github.com/DuncanDoyle/snellius/pkg/computed"
	"github.com/DuncanDoyle/snellius/pkg/explore"
	"github.com/DuncanDoyle/snellius/pkg/objects"
)

// DelegState is a row's delegation state from view C (design §4.2). It is
// zero on a root route's row other than a delegation row.
type DelegState struct {
	// Provenance are the routes that delegated to the row's route, root first.
	Provenance []objects.Ref
	// Matches are the view C effective matches of a delegated row's rule on
	// this path; nil on a root route or where view C has none.
	Matches []objects.Match
	// Unknown is set when view C cannot tell this path's outcome.
	Unknown computed.UnknownReason
	// Unconfirmable, NotComputed and Truncated are the view C row's
	// Unconfirmable, SomeNotComputed and ContextsTruncated: a truncated row
	// has more paths not computed.
	Unconfirmable, NotComputed, Truncated bool
}

// pathCtx is a row's path context: its chain, hops[off:off+n], and its
// delegation state. Rows on one chain with one state share it.
type pathCtx struct {
	matches []objects.Match
	off     uint32
	n       uint16
	// unknown is the unknowns index plus one; 0 for none.
	unknown, flags uint8
}

const (
	flagUnconfirmable uint8 = 1 << iota
	flagNotComputed
	flagTruncated
)

// stateKey is a delegation row's path context: the context of its rule, and its rule and ref.
type stateKey struct{ ctx, rule, ref int32 }

// Delegation is the row's delegation state.
func (t *Topology) Delegation(r Row) DelegState { return t.DelegationFor(r, nil) }

// DelegationFor is the row's delegation state from the view C rows row
// shows; a nil row shows every row.
func (t *Topology) DelegationFor(r Row, row ViewRow) DelegState {
	unknown, flags := t.state(r.ctx, row)
	d := DelegState{Matches: t.ctxs[r.ctx].matches, Unknown: unknown, Unconfirmable: flags&flagUnconfirmable != 0,
		NotComputed: flags&flagNotComputed != 0, Truncated: flags&flagTruncated != 0}
	if c := t.chain(r); len(c) > 1 {
		d.Provenance = make([]objects.Ref, len(c)-1)
		for i, ri := range c[:len(c)-1] {
			d.Provenance[i] = t.Refs[t.routes[ri].ref]
		}
	}
	return d
}

// unknown interns reason and returns its index plus one; 0 for none.
func (b *builder) unknown(reason computed.UnknownReason) uint8 {
	if reason == "" {
		return 0
	}
	i := slices.Index(b.t.unknowns, reason)
	if i < 0 {
		i = len(b.t.unknowns)
		b.t.unknowns = append(b.t.unknowns, reason)
	}
	return uint8(i + 1) // #nosec G115 -- the unknown reasons are a handful of codes
}

// rowFlags are view C row vr's flags.
func rowFlags(vr *computed.Row) uint8 {
	var f uint8
	if vr.Unconfirmable {
		f |= flagUnconfirmable
	}
	if vr.SomeNotComputed {
		f |= flagNotComputed
	}
	if vr.ContextsTruncated {
		f |= flagTruncated
	}
	return f
}

// refState is the path context of the delegation row for ref of rule in
// context ctx: ctx with the union of the ref's view C rows' states.
func (b *builder) refState(ctx int32, rule, ref int) int32 {
	k := stateKey{ctx: ctx, rule: int32(rule), ref: int32(ref)} // #nosec G115 -- rule and ref indexes fit in int32
	if i, ok := b.states[k]; ok {
		return i
	}
	p := b.t.ctxs[ctx]
	parent := b.t.Refs[b.t.routes[b.t.hops[p.off+uint32(p.n)-1]].ref]
	rows := b.in.Views.Delegation.Rows
	var flags uint8
	var unknown computed.UnknownReason
	// Rows are in RowKey order, and the zero child sorts first.
	i, _ := slices.BinarySearchFunc(rows, computed.RowKey{Parent: parent, Rule: rule, Ref: ref}, func(r computed.Row, t computed.RowKey) int {
		return r.Key.Compare(t)
	})
	for ; i < len(rows) && rows[i].Key.Parent == parent && rows[i].Key.Rule == rule && rows[i].Key.Ref == ref; i++ {
		flags |= rowFlags(&rows[i])
		unknown = cmp.Or(unknown, rows[i].Unknown)
	}
	idx := ctx
	// A context of its own whenever the rows set a state, so a viewer's rows can decide it again.
	if flags != 0 || unknown != "" {
		p.flags |= flags
		if p.unknown == 0 {
			p.unknown = b.unknown(unknown)
		}
		idx = b.ctx(p)
		b.source(idx, ctxSource{base: ctx, rows: b.in.Index.RefRows[explore.RefKey{Parent: parent, Rule: rule, Ref: ref}]})
	}
	b.states[k] = idx
	return idx
}

// ctxSource is the view C rows a path context's delegation state comes
// from: base's state, if base is not -1, then rows (Index.DelegRows), then
// context, the unknown of a context that did not attach.
type ctxSource struct {
	base       int32
	rows       []int
	context    computed.UnknownReason
	unattached bool
}

// source records s as context ctx's source.
func (b *builder) source(ctx int32, s ctxSource) {
	if b.t.srcs == nil {
		b.t.srcs = map[int32]ctxSource{}
	}
	b.t.srcs[ctx] = s
}

// ViewRow is Index.DelegRows[i] as one viewer sees it, false when hidden.
type ViewRow func(i int) (*computed.Row, bool)

// state is context ctx's unknown and flags; with row, from the rows row
// shows, as the topology built them from every row.
func (t *Topology) state(ctx int32, row ViewRow) (computed.UnknownReason, uint8) {
	pc := &t.ctxs[ctx]
	s, ok := t.srcs[ctx]
	if row == nil || !ok {
		if pc.unknown == 0 {
			return "", pc.flags
		}
		return t.unknowns[pc.unknown-1], pc.flags
	}
	var unknown, ru computed.UnknownReason
	var flags uint8
	if s.base >= 0 {
		unknown, flags = t.state(s.base, row)
	}
	for _, i := range s.rows {
		if r, ok := row(i); ok {
			flags |= rowFlags(r)
			ru = cmp.Or(ru, r.Unknown)
		}
	}
	unknown = cmp.Or(unknown, ru)
	if s.unattached {
		unknown = cmp.Or(s.context, unknown, computed.UnknownContext)
	}
	return unknown, flags
}

// reach is a child reached under one context path, through one or more
// delegation refs of the path's last route: their view C rows, merged.
type reach struct {
	child objects.Ref
	// c is the context the rows follow, an attached one when any is.
	c       *computed.Context
	flags   uint8
	unknown computed.UnknownReason
	// rows are the Index.DelegRows the flags and unknown come from.
	rows []int
}

type reachKey struct {
	path  *computed.Path
	child objects.Ref
}

// delegated adds the rows of every child view C reaches (design §4.2): for
// each retained context that attached the child, or whose outcome is unknown,
// the child's rules under every attachment of the context's root. Refs of
// one rule that select the same child give its rows once. The contexts bound
// the rows; view C's truncation marks them.
func (b *builder) delegated() {
	if !b.in.Delegates {
		return
	}
	var order []*reach
	byKey := map[reachKey]*reach{}
	// The index's rows are view C's with root eligibility merged into their deps.
	rows := b.in.Index.DelegRows
	for i := range rows {
		vr := &rows[i]
		if vr.Key.Child == (objects.Ref{}) {
			continue
		}
		for ci := range vr.Contexts {
			c := &vr.Contexts[ci]
			if c.Path.Len() == 0 || noTraffic(c.Outcome) {
				continue
			}
			// A context's frame, so its path, is shared by its reporters and by the rule's refs.
			k := reachKey{c.Path, vr.Key.Child}
			r := byKey[k]
			if r == nil {
				r = &reach{child: vr.Key.Child, c: c}
				byKey[k] = r
				order = append(order, r)
			} else if r.c.Outcome != computed.OutcomeAttached {
				r.c = c
			}
			r.flags |= rowFlags(&vr.Row)
			r.unknown = cmp.Or(r.unknown, vr.Unknown)
			if len(r.rows) == 0 || r.rows[len(r.rows)-1] != i {
				r.rows = append(r.rows, i)
			}
		}
	}
	for _, r := range order {
		b.context(r)
	}
}

// noTraffic reports whether a context outcome sends no traffic to the child.
// Any other value than these and attached, empty included, counts as unknown.
func noTraffic(o computed.RowOutcome) bool {
	switch o {
	case computed.OutcomeDropped, computed.OutcomeForbidden, computed.OutcomeRejectedHostnames,
		computed.OutcomeCycle, computed.OutcomeUnresolved:
		return true
	}
	return false
}

// context adds the rows of r's child under its context.
func (b *builder) context(r *reach) {
	steps := r.c.Path.Steps()
	atts := b.roots[steps[0].Route]
	if len(atts) == 0 || len(steps) >= math.MaxUint16 {
		return
	}
	rs := make([]int32, 0, len(steps)+1)
	for _, s := range append(steps, computed.PathStep{Route: r.child}) {
		ri, ok := b.route(s.Route)
		if !ok {
			return
		}
		rs = append(rs, ri)
	}
	// The row reveals every route of its chain and the view B rows of every
	// hop, so whatever decides that a root reaches the child (design §7.4 rule 4).
	extra := make([]int32, 0, len(rs))
	for k, ri := range rs {
		extra = append(extra, b.t.routes[ri].ref)
		if k > 0 {
			extra = append(extra, b.hopDeps(b.t.Refs[b.t.routes[rs[k-1]].ref], b.t.Refs[b.t.routes[ri].ref])...)
		}
	}
	rules := b.t.routes[rs[len(rs)-1]].rules
	p := pathCtx{off: b.chain(rs), n: uint16(len(rs)), flags: r.flags, unknown: b.unknown(r.unknown)} // #nosec G115 -- checked above
	if r.c.Outcome != computed.OutcomeAttached {
		// The child's effective rules are unknown, so every written rule may be on the path.
		p.unknown = b.unknown(cmp.Or(r.c.Unknown, r.unknown, computed.UnknownContext))
		ctx := b.ctx(p)
		b.source(ctx, ctxSource{base: -1, rows: r.rows, context: r.c.Unknown, unattached: true})
		for ri := range rules {
			b.childRule(atts, ctx, ri, &rules[ri], extra)
		}
		return
	}
	for _, er := range r.c.Rules {
		if !er.Kept || er.Index < 0 || er.Index >= len(rules) {
			continue
		}
		p.matches = er.Matches
		ctx := b.ctx(p)
		if r.flags != 0 || r.unknown != "" {
			b.source(ctx, ctxSource{base: -1, rows: r.rows})
		}
		b.childRule(atts, ctx, er.Index, &rules[er.Index], extra)
	}
}

// hopDeps are the deps, as Refs indexes, of every view B row from parent to
// child across its rules and refs, eligibility included.
func (b *builder) hopDeps(parent, child objects.Ref) []int32 {
	k := [2]objects.Ref{parent, child}
	if d, ok := b.hopSets[k]; ok {
		return d
	}
	var d []int32
	ix := b.in.Index
	for _, i := range ix.Delegation[parent] {
		if r := &ix.DelegRows[i]; r.Key.Child == child {
			for _, x := range r.Deps {
				d = append(d, b.ref(ix.Refs[x]))
			}
		}
	}
	slices.Sort(d)
	d = slices.Compact(d)
	b.hopSets[k] = d
	return d
}

// childRule adds the rows of a delegated rule under every root attachment atts.
func (b *builder) childRule(atts []rootAtt, ctx int32, ri int, r *objects.Rule, extra []int32) {
	for _, a := range atts {
		b.base = append(append(b.base[:0], a.base...), extra...)
		b.rule(a.att, ctx, ri, r, b.base)
	}
}

// Delegation is row r of t's delegation state as p's viewer sees it.
func (p *Projection) Delegation(t *Topology, r Row) DelegState {
	var row ViewRow
	if p.filter != nil {
		row = p.filter.Deleg
	}
	return t.DelegationFor(r, row)
}
