package core

import (
	"math"
	"time"

	p "github.com/siemendev/dzstudio-collab-server/protocol"
)

type known struct {
	participants, locks, live, notices, saving int64
}

func (s *Service) hPoll(r *p.R, _ RequestContext, u *User) *p.Obj {
	after := r.Int("after", 0, p.MaxInt)
	waitMs := min(r.OptInt("waitMs", 5000, 0, 8000), s.limits.MaxWaitMs)
	maxBytes := int(r.OptInt("maxBytes", 2*p.MB, 0, ResponseBytes))
	var k known
	if v := r.OptObject("versions"); v != nil {
		k.participants = v.OptInt("participants", 0, 0, p.MaxInt)
		k.locks = v.OptInt("locks", 0, 0, p.MaxInt)
		k.live = v.OptInt("live", 0, 0, p.MaxInt)
		k.notices = v.OptInt("notices", 0, 0, p.MaxInt)
		k.saving = v.OptInt("saving", 0, 0, p.MaxInt)
	}
	presence := r.OptObject("presence")
	var position *[3]float64
	var idle *float64
	if presence != nil {
		if presence.Has("position") {
			v := p.ToVec3(presence.M["position"], "presence.position")
			position = &v
		}
		if presence.Has("idleSeconds") {
			f := math.Max(0, p.ToNumber(presence.M["idleSeconds"], "presence.idleSeconds"))
			idle = &f
		}
	}
	saving := r.OptObject("saving")
	var savingState string
	var savingCount int64
	if saving != nil {
		savingState = saving.OneOf("state", "saved", "unsaved", "saving", "failed")
		savingCount = saving.OptInt("unsavedCount", 0, 0, p.MaxInt)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	ss, pt := s.member(r, *u)
	var agent *agentPresence
	if pt.isAgent() && s.opts.Capabilities.Agents {
		agent = readAgentPresence(presence)
	}
	if after > ss.committed {
		p.BadRequestf("after is beyond the highest seq")
	}
	if after < ss.truncatedThrough {
		p.Fail(p.CursorTooOld, "")
	}
	if pt.cut != nil {
		close(pt.cut)
		pt.cut = nil
	}
	pt.Cursor = after
	if position != nil {
		pt.position = position
	}
	if idle != nil {
		pt.idleReported = *idle
		pt.idleReportedAt = s.now()
	}
	if agent != nil {
		pt.phase, pt.status = agent.phase, agent.status
	}
	if saving != nil && ss.Kind == "local" && pt.Role == "host" {
		s.applyLocalSaving(ss, savingState, savingCount)
	}
	if !pt.Visible {
		pt.Visible = true
		ss.ver.Participants++
		s.dirty(ss)
		s.notify(ss)
	}
	s.updateLapse(ss)
	s.maybeAskCheckpoint(ss)

	deadline := time.Now().Add(time.Duration(waitMs) * time.Millisecond)
	for {
		s.checkStillIn(ss, pt)
		ready := ss.committed > after ||
			ss.ver.Participants > k.participants || ss.ver.Locks > k.locks || ss.ver.Live > k.live ||
			ss.ver.Saving > k.saving || pt.NoticeVer > k.notices ||
			(ss.checkpointFrom == pt.ID && !pt.checkpointSent)
		remaining := time.Until(deadline)
		if ready || remaining <= 0 || s.stopped {
			break
		}
		wake := ss.wake
		cut := make(chan struct{})
		pt.cut = cut
		s.mu.Unlock()
		timer := time.NewTimer(remaining)
		wasCut := false
		select {
		case <-wake:
		case <-timer.C:
		case <-cut:
			wasCut = true
		}
		timer.Stop()
		s.mu.Lock()
		if pt.cut == cut {
			pt.cut = nil
		}
		if wasCut {
			break
		}
	}
	s.checkStillIn(ss, pt)
	out := s.pollAnswer(ss, pt, after, maxBytes, k)
	s.reserveVersions(ss)
	return out
}

func (s *Service) checkStillIn(ss *session, pt *participant) {
	if ss.ended != nil {
		failEnded(ss.ended)
	}
	if pt.Status != "active" {
		failNotParticipant(pt)
	}
}

func (s *Service) applyLocalSaving(ss *session, state string, count int64) {
	cur := &ss.local
	if cur.State == state && cur.UnsavedCount == count {
		return
	}
	if state == "saved" && cur.State != "saved" {
		now := s.now()
		cur.SavedAt = &now
	}
	cur.State = state
	cur.UnsavedCount = count
	ss.ver.Saving++
	s.dirty(ss)
	s.notify(ss)
}

func (s *Service) pollAnswer(ss *session, pt *participant, after int64, maxBytes int, k known) *p.Obj {
	out := p.O("ok", true, "seq", ss.committed, "more", false)
	if ss.ver.Participants > k.participants {
		list := []*p.Obj{}
		for _, x := range s.visible(ss) {
			list = append(list, s.participantView(x))
		}
		out.Set("participants", p.O("version", ss.ver.Participants, "list", list))
	}
	pres := []*p.Obj{}
	for _, x := range s.visible(ss) {
		pres = append(pres, s.presenceView(x))
	}
	out.Set("presence", pres)
	if ss.ver.Saving > k.saving {
		out.Set("saving", s.savingView(ss))
	}
	if ss.ver.Locks > k.locks {
		out.Set("locks", s.locksView(ss))
	}
	if ss.ver.Live > k.live {
		out.Set("live", s.liveView(ss))
	}
	if pt.NoticeVer > k.notices {
		list := []*p.Obj{}
		for _, n := range pt.notices {
			if n.version > k.notices {
				list = append(list, noticeView(n.n))
			}
		}
		out.Set("notices", p.O("version", pt.NoticeVer, "list", list))
	}
	if ss.checkpointFrom == pt.ID {
		out.Set("checkpointWanted", true)
		pt.checkpointSent = true
	}

	ops := []*p.Obj{}
	more := false
	if ss.committed > after {
		const margin = 4096
		fields := p.SerializedBytes(out)
		budget := min(maxBytes, ResponseBytes-margin) - fields
		used := 0
		cursor := after
	outer:
		for cursor < ss.committed {
			entries := must(s.deps.Storage.Read(ss.ID, cursor, 256))
			if len(entries) == 0 {
				break
			}
			for _, e := range entries {
				if e.Seq > ss.committed {
					break outer
				}
				views := opViews(e)
				size := 0
				for _, v := range views {
					size += p.SerializedBytes(v) + 1
				}
				if len(ops) == 0 && fields+size > ResponseBytes-margin {
					for _, f := range []string{"participants", "locks", "live", "notices", "saving"} {
						out.Del(f)
					}
					more = true
					fields = p.SerializedBytes(out)
					budget = min(maxBytes, ResponseBytes-margin) - fields
				}
				if len(ops) > 0 && used+size > budget {
					more = true
					break outer
				}
				ops = append(ops, views...)
				used += size
				cursor = e.Seq
			}
		}
	}
	out.Set("more", more)
	out.Set("ops", ops)
	return out
}

func opViews(e LogEntry) []*p.Obj {
	out := make([]*p.Obj, len(e.Parts))
	for i, pieces := range e.Parts {
		v := p.O("seq", e.Seq, "participantId", e.ParticipantID, "opId", e.OpID)
		if e.System {
			v.Set("system", true)
		}
		if e.LiveID != "" {
			v.Set("liveId", e.LiveID)
		}
		if len(e.Parts) > 1 {
			v.Set("part", i)
			v.Set("parts", len(e.Parts))
		}
		if e.Chain != nil {
			v.Set("chain", p.O("index", e.Chain.Index, "count", e.Chain.Count))
		}
		if pieces == nil {
			pieces = []string{}
		}
		v.Set("pieces", pieces)
		out[i] = v
	}
	return out
}

func noticeView(n notice) *p.Obj {
	switch n.Kind {
	case "locks_taken":
		return p.O("kind", n.Kind, "by", n.By, "count", n.Count)
	case "participant_removed":
		return p.O("kind", n.Kind, "participantId", n.ParticipantID, "name", n.Name, "by", n.By)
	case "attachment_wanted":
		return p.O("kind", n.Kind, "checksum", n.Checksum)
	}
	return p.O("kind", n.Kind, "by", n.By)
}

func vec(v [3]float64) []p.F32 { return []p.F32{p.F32(v[0]), p.F32(v[1]), p.F32(v[2])} }

func (s *Service) presenceView(pt *participant) *p.Obj {
	age := s.contactAge(pt)
	idle := s.idleNow(pt)
	state := "active"
	switch {
	case age >= graceMs:
		state = "disconnected"
	case age >= interruptedMs:
		state = "interrupted"
	case idle >= idleS:
		state = "idle"
	}
	v := p.O("participantId", pt.ID, "state", state, "idleSeconds", int64(idle))
	if pt.position != nil {
		v.Set("position", vec(*pt.position))
	}
	v.Set("lastSeenSeconds", age/1000)
	if pt.isAgent() && s.opts.Capabilities.Agents {
		v.Set("phase", s.agentPhase(pt))
		v.Set("status", pt.status)
	}
	return v
}

func (s *Service) savingView(ss *session) *p.Obj {
	now := s.now()
	if ss.Kind == "local" {
		v := p.O("version", ss.ver.Saving, "state", ss.local.State, "unsavedCount", ss.local.UnsavedCount)
		if ss.local.SavedAt != nil {
			v.Set("savedSecondsAgo", (now-*ss.local.SavedAt)/1000)
		}
		return v
	}
	h := ss.hosted
	unsaved := s.unsavedCount(ss)
	state := "saved"
	if h.writing {
		state = "saving"
	} else if unsaved > 0 {
		state = "unsaved"
	}
	v := p.O("version", ss.ver.Saving, "state", state, "savedSeq", h.SavedSeq, "unsavedCount", unsaved)
	if h.SavedBy != nil {
		v.Set("savedBy", userObj(*h.SavedBy))
	}
	if h.SavedAt != nil {
		v.Set("savedSecondsAgo", (now-*h.SavedAt)/1000)
	}
	return v
}

func (s *Service) unsavedCount(ss *session) int64 {
	if ss.Kind != "hosted" {
		return 0
	}
	return max(0, ss.ns-ss.hosted.SavedNS)
}

func (s *Service) locksView(ss *session) *p.Obj {
	holders := []*p.Obj{}
	for _, pt := range s.active(ss) {
		if len(pt.locks) == 0 {
			continue
		}
		els := make([]string, 0, len(pt.locks))
		for el := range pt.locks {
			els = append(els, el)
		}
		holders = append(holders, p.O("participantId", pt.ID, "lapsed", pt.lapsed, "elements", els))
	}
	return p.O("version", ss.ver.Locks, "holders", holders)
}

func (s *Service) liveView(ss *session) *p.Obj {
	states := []*p.Obj{}
	for _, pt := range s.active(ss) {
		l := pt.live
		if l == nil {
			continue
		}
		v := p.O("participantId", pt.ID, "liveId", l.LiveID, "n", l.N, "mode", l.Mode)
		if l.Mode == "values" {
			vals := []*p.Obj{}
			for _, x := range l.Values {
				vals = append(vals, p.O("element", x.Element, "position", vec(x.Position), "ypr", vec(x.YPR), "scale", p.F32(x.Scale)))
			}
			v.Set("values", vals)
		}
		if l.Transform != nil {
			v.Set("transform", p.O("pivot", vec(l.Transform.Pivot), "translate", vec(l.Transform.Translate), "yaw", p.F32(l.Transform.Yaw)))
		}
		if l.Committed {
			v.Set("committed", true)
		}
		states = append(states, v)
	}
	return p.O("version", ss.ver.Live, "states", states)
}
