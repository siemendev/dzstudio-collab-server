package core

import (
	"fmt"
	"strconv"

	p "github.com/siemendev/dzstudio-collab-server/protocol"
)

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// catch runs f and gives back the protocol error it raised, if any.
func catch(f func()) (err *p.Error) {
	defer func() {
		if r := recover(); r != nil {
			if pe, ok := r.(*p.Error); ok {
				err = pe
				return
			}
			panic(r)
		}
	}()
	f()
	return nil
}

func opKey(opID string, c *Chain) string {
	if c == nil {
		return opID
	}
	return opID + "\x00" + itoa(int64(c.Index))
}

func sameChain(a, b *Chain) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Count == b.Count && a.Index == b.Index
}

func partsBytes(parts [][]string) int64 {
	var n int64
	for _, ps := range parts {
		n += int64(p.PiecesBytes(ps))
	}
	return n
}

type opsRun struct {
	ss       *session
	pt       *participant
	results  []*p.Obj
	reported map[int64]bool
	fresh    []LogEntry
	staged   []StagedItem
	reset    bool
}

func (o *opsRun) report(clientSeq, seq int64) {
	if o.reported[clientSeq] {
		return
	}
	o.reported[clientSeq] = true
	o.results = append(o.results, p.O("clientSeq", clientSeq, "seq", seq))
}

func (o *opsRun) stage(it StagedItem) {
	o.staged = append(o.staged, it)
	o.pt.staged = append(o.pt.staged, it)
}

// unstage forgets the staged items of these clientSeqs (their operation is appended or refused).
func (o *opsRun) unstage(clientSeqs ...int64) {
	drop := map[int64]bool{}
	for _, c := range clientSeqs {
		drop[c] = true
	}
	kept := o.pt.staged[:0:0]
	for _, it := range o.pt.staged {
		if !drop[it.ClientSeq] {
			kept = append(kept, it)
		}
	}
	if len(kept) != len(o.pt.staged) {
		o.reset = true
	}
	o.pt.staged = kept
}

func (s *Service) nextEntry(o *opsRun, clientSeq int64, opID string, system bool, liveID string, c *Chain, parts [][]string, countNS bool) int64 {
	ss := o.ss
	ss.head++
	seq := ss.head
	if countNS {
		ss.ns++
	}
	e := LogEntry{Seq: seq, ParticipantID: o.pt.ID, ClientSeq: clientSeq, OpID: opID, System: system, LiveID: liveID, Chain: c, Parts: parts, NS: ss.ns}
	o.fresh = append(o.fresh, e)
	ss.opIDs[opKey(opID, c)] = seq
	o.pt.clientSeqToSeq[clientSeq] = seq
	ss.opsSinceSnap++
	ss.bytesSinceSnap += partsBytes(parts)
	return seq
}

func (s *Service) hOpsSend(r *p.R, _ RequestContext, u *User) *p.Obj {
	items := r.Array("ops", 200)
	s.mu.Lock()
	defer s.mu.Unlock()
	ss, pt := s.member(r, *u)
	o := &opsRun{ss: ss, pt: pt, results: []*p.Obj{}, reported: map[int64]bool{}}
	var failure *p.Error
	for i, raw := range items {
		if failure = catch(func() { s.opItem(o, i, raw) }); failure != nil {
			break
		}
	}
	if len(o.fresh) > 0 {
		s.appendEntries(ss, o.fresh)
	}
	if o.reset {
		must(0, s.deps.Storage.Unstage(ss.ID, pt.ID))
		if len(pt.staged) > 0 {
			must(0, s.deps.Storage.Stage(ss.ID, pt.ID, pt.staged))
		}
	} else if len(o.staged) > 0 {
		must(0, s.deps.Storage.Stage(ss.ID, pt.ID, o.staged))
	}
	if len(o.fresh) > 0 || len(o.staged) > 0 || o.reset {
		s.dirty(ss)
	}
	if failure != nil {
		panic(failure)
	}
	return p.O("ok", true, "results", o.results, "nextClientSeq", pt.NextSeq)
}

func (s *Service) opItem(o *opsRun, i int, raw any) {
	ss, pt := o.ss, o.pt
	ir := p.Item(raw, fmt.Sprintf("ops[%d]", i))
	clientSeq := ir.Int("clientSeq", 1, p.MaxInt)
	opID := ir.String("opId", 64, 1)
	system := ir.Bool("system")
	liveID, _ := ir.OptString("liveId", 64)
	if ir.Has("part") != ir.Has("parts") {
		p.BadRequestf("part and parts come together")
	}
	parts := int(ir.OptInt("parts", 1, 1, 100_000))
	part := int(ir.OptInt("part", 0, 0, int64(parts-1)))
	var chain *Chain
	if cr := ir.OptObject("chain"); cr != nil {
		count := cr.Int("count", 0, p.MaxInt)
		index := cr.Int("index", 0, p.MaxInt)
		if count < 2 {
			p.BadRequestf("a chain has at least 2 links")
		}
		if count > MaxChainOps {
			p.Fail(p.TooLarge, fmt.Sprintf("a chain has at most %d links", MaxChainOps), "maxCount", MaxChainOps)
		}
		if index >= count {
			p.BadRequestf("chain.index must be below chain.count")
		}
		chain = &Chain{Index: int(index), Count: int(count)}
	}
	pieces, bytes := p.Pieces(ir.M["pieces"], "pieces")
	if bytes > PartBytes {
		p.Fail(p.TooLarge, "part over 1 MB", "maxBytes", PartBytes)
	}

	if clientSeq < pt.NextSeq {
		if seq, ok := pt.clientSeqToSeq[clientSeq]; ok {
			o.report(clientSeq, seq)
		} else if !pt.chainHas(clientSeq) {
			o.report(clientSeq, ss.truncatedThrough)
		}
		return
	}
	if pd := pt.pending; pd != nil {
		if clientSeq != pd.ClientSeq || parts == 1 || parts != len(pd.Parts) {
			p.Fail(p.BatchPending, "the previous operation is still missing parts")
		}
		if opID != pd.OpID || system != pd.System || liveID != pd.LiveID || !sameChain(chain, pd.Chain) {
			p.BadRequestf("all parts of an operation carry the same opId, system, liveId and chain")
		}
	}
	if pt.pending == nil && pt.chain != nil && chain == nil {
		p.Fail(p.BatchPending, "a chain is still open")
	}
	if clientSeq > pt.NextSeq {
		p.Fail(p.ClientSeqGap, "", "expectedClientSeq", pt.NextSeq)
	}
	key := opKey(opID, chain)
	if pt.pending == nil {
		_, known := ss.opIDs[key]
		if !(known && pt.chain == nil) {
			if oc := pt.chain; oc != nil {
				if chain.Index != len(oc.Links) || opID != oc.OpID || chain.Count != oc.Count || system != oc.System || liveID != oc.LiveID {
					p.BadRequestf("this link does not continue the open chain")
				}
			} else if chain != nil && chain.Index != 0 {
				p.BadRequestf("a chain starts with index 0")
			}
		}
	}

	item := StagedItem{ClientSeq: clientSeq, OpID: opID, System: system, LiveID: liveID, Chain: chain, Part: part, Parts: parts, Pieces: pieces}
	var complete [][]string
	if parts > 1 {
		pd := pt.pending
		if pd == nil {
			pd = &pendingOp{ClientSeq: clientSeq, OpID: opID, System: system, LiveID: liveID, Chain: chain,
				Parts: make([][]string, parts), Have: make([]bool, parts)}
			pt.pending = pd
		}
		if !pd.Have[part] {
			if pd.Bytes+bytes > MaxOpBytes {
				pt.pending = nil
				o.unstage(clientSeq)
				p.Fail(p.TooLarge, "operation too large", "maxBytes", MaxOpBytes)
			}
			pd.Parts[part] = pieces
			pd.Have[part] = true
			pd.Bytes += bytes
			o.stage(item)
		}
		for _, h := range pd.Have {
			if !h {
				return
			}
		}
		pt.pending = nil
		complete = pd.Parts
	} else {
		complete = [][]string{pieces}
	}

	if seq, known := ss.opIDs[key]; known && pt.chain == nil {
		pt.clientSeqToSeq[clientSeq] = seq
		pt.NextSeq = clientSeq + 1
		o.report(clientSeq, seq)
		o.unstage(clientSeq)
		return
	}
	if chain == nil {
		seq := s.nextEntry(o, clientSeq, opID, system, liveID, nil, complete, !system)
		pt.NextSeq = clientSeq + 1
		o.report(clientSeq, seq)
		o.unstage(clientSeq)
		return
	}
	if pt.chain == nil {
		pt.chain = &openChain{OpID: opID, Count: chain.Count, System: system, LiveID: liveID}
	}
	pt.chain.Links = append(pt.chain.Links, chainLink{ClientSeq: clientSeq, Parts: complete})
	pt.NextSeq = clientSeq + 1
	if parts == 1 {
		o.stage(item)
	}
	if chain.Index < chain.Count-1 {
		return
	}
	oc := pt.chain
	pt.chain = nil
	seqs := make([]int64, len(oc.Links))
	for i, l := range oc.Links {
		last := i == len(oc.Links)-1
		seq := s.nextEntry(o, l.ClientSeq, oc.OpID, oc.System, oc.LiveID, &Chain{Index: i, Count: oc.Count}, l.Parts, last && !oc.System)
		o.report(l.ClientSeq, seq)
		seqs[i] = l.ClientSeq
	}
	o.unstage(seqs...)
}

func (pt *participant) chainHas(clientSeq int64) bool {
	if pt.chain == nil {
		return false
	}
	for _, l := range pt.chain.Links {
		if l.ClientSeq == clientSeq {
			return true
		}
	}
	return false
}

// dropUnappended forgets accepted parts and links of a participant who leaves, is removed or timed out.
func (s *Service) dropUnappended(ss *session, pt *participant) {
	if pt.pending == nil && pt.chain == nil && len(pt.staged) == 0 {
		return
	}
	pt.pending = nil
	pt.chain = nil
	pt.staged = nil
	if err := s.deps.Storage.Unstage(ss.ID, pt.ID); err != nil {
		s.logError("could not drop staged items", err)
	}
}

func (s *Service) appendEntries(ss *session, entries []LogEntry) {
	must(0, s.deps.Storage.Append(ss.ID, entries))
	ss.committed = entries[len(entries)-1].Seq
	liveChanged := false
	for _, e := range entries {
		if e.LiveID == "" {
			continue
		}
		if pt := ss.participants[e.ParticipantID]; pt != nil && pt.live != nil && pt.live.LiveID == e.LiveID {
			pt.live = nil
			liveChanged = true
		}
	}
	if liveChanged {
		ss.ver.Live++
	}
	if ss.Kind == "hosted" {
		for _, e := range entries {
			if !e.System {
				ss.ver.Saving++
				break
			}
		}
	}
	s.dirty(ss)
	s.notify(ss)
	s.maybeAskCheckpoint(ss)
}
