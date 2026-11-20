package emit

import (
	"fmt"
	"regexp"
	"strconv"

	"github.com/getkiko/kiko/app/mygo/internal/model"
	"github.com/getkiko/kiko/app/mygo/internal/schema"
)

// This file emits proxy chains (model.Outbound.Chain). The owner's detour
// is the last hop; every hop after the first is emitted again as a copy
// whose detour is the hop before it, so the hop's own outbound, which
// other chains and rules use as it is, keeps its own detour.

// ChainCopySuffix is the mark ending the tag of a hop's copy, "<hop tag>
// ⛓<n>": views map a copy back to its hop by it (ChainCopyHop).
const ChainCopySuffix = " ⛓"

var chainCopyTag = regexp.MustCompile(` ⛓\d+$`)

// ChainCopyHop is the tag of the hop a copy's tag stands for, and whether
// tag names a copy at all.
func ChainCopyHop(tag string) (string, bool) {
	if loc := chainCopyTag.FindStringIndex(tag); loc != nil {
		return tag[:loc[0]], true
	}
	return tag, false
}

// chainState holds the copies emitted so far.
type chainState struct {
	// copies are the copies' tags by hop id and the tag of the hop
	// before it: chains sharing a prefix share its copies.
	copies map[string]string
	// used are the tags taken in the outbounds' namespace, the copies'
	// included, seeded with every real tag before the first copy.
	used orderedSet
	// pending are the copies not yet added to the outbounds.
	pending []any
}

// chainDetour is the detour of an outbound or endpoint with chain, ""
// without one: the last hop, a copy for a chain of two hops or more.
// owner names the outbound or endpoint in errors; ok is false when a hop
// is not usable, the error recorded.
func (e *emitter) chainDetour(owner string, chain []string) (string, bool) {
	if len(chain) == 0 {
		return "", true
	}
	if e.chain.copies == nil {
		e.chain.copies = map[string]string{}
		for _, tag := range e.tagOf {
			e.chain.used.add(tag)
		}
		for _, tag := range e.emptyGroups {
			e.chain.used.add(tag)
		}
	}
	prev, ok := e.resolve(chain[0])
	if !ok || prev == "" {
		e.errors = append(e.errors, e.chainHopError(owner, 0, chain[0]))
		return "", false
	}
	for i, id := range chain[1:] {
		key := id + "\x00" + prev
		if tag, done := e.chain.copies[key]; done {
			prev = tag
			continue
		}
		hop := e.outboundByID[id]
		hopTag, live := e.tagOf[id]
		if hop == nil || !live || !model.ChainMiddleHop(model.Target{Outbound: hop}) {
			e.errors = append(e.errors, e.chainHopError(owner, i+1, id))
			return "", false
		}
		tag := e.claimChainCopy(hopTag)
		o := e.outboundObject(hop, tag, "出站「"+hopTag+"」")
		o.set("detour", prev)
		e.chain.copies[key] = tag
		e.chain.pending = append(e.chain.pending, prune(o))
		prev = tag
	}
	return prev, true
}

// claimChainCopy is a free tag for a copy of the hop tagged hop.
func (e *emitter) claimChainCopy(hop string) string {
	for n := 1; ; n++ {
		tag := hop + ChainCopySuffix + strconv.Itoa(n)
		if !e.chain.used.has(tag) {
			e.chain.used.add(tag)
			return tag
		}
	}
}

// flushChainCopies adds the copies emitted since the last call to the
// outbounds.
func (e *emitter) flushChainCopies() {
	e.outbounds = append(e.outbounds, e.chain.pending...)
	e.chain.pending = nil
}

// chainHopError is the error of hop index of owner's chain, which does not
// resolve or cannot stand there: emitting without it would skip the hops
// the user chose.
func (e *emitter) chainHopError(owner string, index int, id string) string {
	prefix := fmt.Sprintf("%s的代理链第 %d 跳", owner, index+1)
	target := model.FindOutboundTarget(e.doc, id)
	if tag, empty := e.emptyGroups[id]; empty {
		return prefix + "「" + tag + "」是没有可用成员的分组，请添加成员或重新选择"
	}
	if !target.Valid() {
		return prefix + "已删除，请重新选择"
	}
	if _, live := e.tagOf[id]; !live {
		return prefix + "「" + orUnnamed(target.Tag()) + "」不可用，请启用它或重新选择"
	}
	return prefix + "「" + orUnnamed(target.Tag()) + "」不是可以转发连接的服务器，请重新选择"
}

// outboundObject is a server outbound's configuration under tag, without
// a detour: its chain is the caller's. label names it in notes of
// skipped fields.
func (e *emitter) outboundObject(ob *model.Outbound, tag, label string) *object {
	o := obj("type", ob.Type, "tag", tag)
	applyFields(o, ob.Fields, schema.OutboundSchema[ob.Type], e.resolve, e.platform, e.noteSkip(label), nil, e.resolveDNS)
	postProcessOutbound(ob.Type, o, ob.Fields)
	o.del("detour")
	return o
}
