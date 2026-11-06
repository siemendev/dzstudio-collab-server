package core

import (
	"encoding/json"

	p "github.com/siemendev/dzstudio-collab-server/protocol"
)

func (s *Service) updateLapse(ss *session) {
	changed := false
	for _, pt := range s.active(ss) {
		lapsed := s.idleNow(pt) >= lockLapseS
		if lapsed != pt.lapsed {
			pt.lapsed = lapsed
			if len(pt.locks) > 0 {
				changed = true
			}
		}
	}
	if changed {
		ss.ver.Locks++
		s.notify(ss)
	}
}

func (s *Service) releaseLocks(ss *session, pt *participant) bool {
	if len(pt.locks) == 0 {
		return false
	}
	for el := range pt.locks {
		if ss.lockOwner[el] == pt.ID {
			delete(ss.lockOwner, el)
		}
	}
	ss.lockCount -= len(pt.locks)
	pt.locks = map[string]struct{}{}
	return true
}

func lockAnswerObj(a *lockAnswer) *p.Obj {
	denied := []*p.Obj{}
	for _, d := range a.Denied {
		if d.Holder == nil {
			denied = append(denied, p.O("element", d.Element, "holder", p.Raw("null")))
		} else {
			denied = append(denied, p.O("element", d.Element, "holder", *d.Holder))
		}
	}
	granted := a.Granted
	if granted == nil {
		granted = []string{}
	}
	return p.O("ok", true, "lockSeq", a.LockSeq, "granted", granted, "denied", denied, "version", a.Version)
}

func (s *Service) hLocksSet(r *p.R, _ RequestContext, u *User) *p.Obj {
	lockSeq := r.Int("lockSeq", 1, p.MaxInt)
	raw := r.Array("elements", maxLocksPerPart)
	wanted := make([]string, 0, len(raw))
	seen := map[string]bool{}
	for _, el := range raw {
		e, ok := el.(string)
		if !ok || e == "" || len(e) > 64 {
			p.BadRequestf("elements must be ids of at most 64 bytes")
		}
		if !seen[e] {
			seen[e] = true
			wanted = append(wanted, e)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ss, pt := s.member(r, *u)
	if lockSeq <= pt.LockSeq {
		last := pt.lastLockAnswer
		if last == nil {
			last = &lockAnswer{LockSeq: pt.LockSeq, Version: ss.ver.Locks}
		}
		return lockAnswerObj(last)
	}
	s.updateLapse(ss)
	changed := false
	for el := range pt.locks {
		if !seen[el] {
			delete(pt.locks, el)
			delete(ss.lockOwner, el)
			ss.lockCount--
			changed = true
		}
	}
	a := &lockAnswer{LockSeq: lockSeq, Granted: []string{}, Denied: []lockDenial{}}
	taken := map[*participant]int{}
	for _, el := range wanted {
		ownerID, held := ss.lockOwner[el]
		if held && ownerID == pt.ID {
			a.Granted = append(a.Granted, el)
			continue
		}
		owner := ss.participants[ownerID]
		if held && owner != nil && owner.Status == "active" && !owner.lapsed {
			holder := owner.ID
			a.Denied = append(a.Denied, lockDenial{Element: el, Holder: &holder})
			continue
		}
		if held && owner != nil {
			delete(owner.locks, el)
			ss.lockCount--
			taken[owner]++
			delete(ss.lockOwner, el)
		}
		if ss.lockCount >= maxLocksPerSession {
			a.Denied = append(a.Denied, lockDenial{Element: el})
			continue
		}
		ss.lockOwner[el] = pt.ID
		pt.locks[el] = struct{}{}
		ss.lockCount++
		a.Granted = append(a.Granted, el)
		changed = true
	}
	for owner, n := range taken {
		s.pushNotice(owner, notice{Kind: "locks_taken", By: pt.User.Name, Count: n})
	}
	if changed || len(taken) > 0 {
		ss.ver.Locks++
		s.notify(ss)
	}
	pt.LockSeq = lockSeq
	a.Version = ss.ver.Locks
	pt.lastLockAnswer = a
	s.reserveVersions(ss)
	data, _ := json.Marshal(a)
	must(0, s.deps.Storage.PutValue(ss.ID, "locks-"+pt.ID, data))
	s.dirty(ss)
	return lockAnswerObj(a)
}

func (s *Service) hLocksRelease(r *p.R, _ RequestContext, u *User) *p.Obj {
	target := r.ID("target")
	s.mu.Lock()
	defer s.mu.Unlock()
	ss, pt := s.member(r, *u)
	if !pt.CanManage {
		p.Fail(p.NotAllowed, "only who manages the session releases locks")
	}
	if t := ss.participants[target]; t != nil && t.Status == "active" {
		if s.releaseLocks(ss, t) {
			ss.ver.Locks++
		}
		s.pushNotice(t, notice{Kind: "locks_released", By: pt.User.Name})
		s.dirty(ss)
		s.notify(ss)
	}
	return p.O("ok", true)
}

// ------------------------------------------------------------ live

func (s *Service) hLivePut(r *p.R, _ RequestContext, u *User) *p.Obj {
	liveID := r.String("liveId", 64, 1)
	n := r.Int("n", 1, p.MaxInt)
	mode := r.OneOf("mode", "values", "transform", "end")
	var state *liveState
	var end string
	switch mode {
	case "end":
		end = r.OneOf("end", "commit", "cancel")
	case "values":
		list := r.Array("values", 100)
		vals := make([]liveValue, len(list))
		for i, v := range list {
			vr := p.Item(v, "values")
			vals[i] = liveValue{
				Element:  vr.String("element", 64, 1),
				Position: p.ToVec3(vr.M["position"], "values.position"),
				YPR:      p.ToVec3(vr.M["ypr"], "values.ypr"),
				Scale:    p.ToNumber(vr.M["scale"], "values.scale"),
			}
		}
		state = &liveState{LiveID: liveID, N: n, Mode: mode, Values: vals}
	case "transform":
		t := r.Object("transform")
		state = &liveState{LiveID: liveID, N: n, Mode: mode, Transform: &liveTransform{
			Pivot:     p.ToVec3(t.M["pivot"], "transform.pivot"),
			Translate: p.ToVec3(t.M["translate"], "transform.translate"),
			Yaw:       p.ToNumber(t.M["yaw"], "transform.yaw"),
		}}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ss, pt := s.member(r, *u)
	now := s.now()
	if mode == "end" {
		if pt.live != nil && pt.live.LiveID == liveID && !(end == "commit" && pt.live.Committed) {
			if end == "cancel" {
				pt.live = nil
			} else {
				pt.live.Committed = true
				pt.live.CommittedAt = now
			}
			ss.ver.Live++
			s.notify(ss)
		}
		if !pt.hasLastLive || pt.lastLiveID != liveID || n > pt.lastLiveN {
			pt.hasLastLive, pt.lastLiveID, pt.lastLiveN = true, liveID, n
		}
		return p.O("ok", true, "accepted", true)
	}
	if pt.hasLastLive && pt.lastLiveID == liveID && n <= pt.lastLiveN {
		return p.O("ok", true, "accepted", false)
	}
	if pt.lastLiveAt != 0 && now-pt.lastLiveAt < liveMinGapMs {
		return p.O("ok", true, "accepted", false)
	}
	pt.lastLiveAt = now
	pt.hasLastLive, pt.lastLiveID, pt.lastLiveN = true, liveID, n
	pt.live = state
	ss.ver.Live++
	s.notify(ss)
	return p.O("ok", true, "accepted", true)
}
