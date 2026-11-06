package core

import "sort"

// sweep runs every time rule; s.mu is held.
func (s *Service) sweep() {
	now := s.now()
	for id, ss := range s.sessions {
		if ss.ended != nil {
			if now-ss.ended.At > endedKeepMs {
				s.forget(id)
			}
			continue
		}
		if ss.snapshot == nil {
			creator := ss.participants[ss.CreatorID]
			if creator == nil || creator.Status != "active" || s.contactAge(creator) >= creatorGoneMs {
				s.logInfo("session %s discarded: snapshot 0 never completed", id)
				s.forget(id)
				continue
			}
		}
		if ss.lastActive-ss.activeSaved > 3600_000 {
			s.dirty(ss)
		}
		s.sweepSession(ss, now)
	}
	for key, c := range s.requests {
		if c.answer != nil && now-c.at > requestKeepMs {
			delete(s.requests, key)
		}
	}
	if s.requestsSaved > 2*len(s.requests)+200 {
		keep := []StoredRequest{}
		for key, c := range s.requests {
			if c.answer != nil {
				if ok, _ := c.answer.Get("ok"); ok == true {
					keep = append(keep, StoredRequest{Key: key, At: c.at, Answer: serialize(c.answer)})
				}
			}
		}
		if err := s.deps.Storage.CompactRequests(keep); err != nil {
			s.logError("could not compact the retry window", err)
		} else {
			s.requestsSaved = len(keep)
		}
	}
	for code, inv := range s.invites {
		if now >= inv.ExpiresAt+endedKeepMs {
			delete(s.invites, code)
			if ss := s.sessions[inv.SessionID]; ss != nil {
				s.dirty(ss)
			}
		}
	}
	for id, seen := range s.userSeen {
		if now-seen.at > 24*3600_000 {
			delete(s.userSeen, id)
		}
	}
}

func (s *Service) forget(id string) {
	ss := s.sessions[id]
	delete(s.sessions, id)
	for code, inv := range s.invites {
		if inv.SessionID == id {
			delete(s.invites, code)
		}
	}
	if ss != nil {
		for _, pt := range ss.participants {
			s.cutPoll(pt)
		}
		s.notify(ss)
		if ss.ended == nil && ss.HostedRef != nil && s.deps.Hooks != nil {
			go s.deps.Hooks.HostedEnded(ss.ID, *ss.HostedRef)
		}
	}
	if err := s.deps.Attachments.DropSession(id); err != nil {
		s.logError("could not delete the attachments of "+id, err)
	}
	if err := s.deps.Storage.DeleteSession(id); err != nil {
		s.logError("could not delete session "+id, err)
	}
}

func (s *Service) sweepSession(ss *session, now int64) {
	changed := false
	s.updateLapse(ss)
	for _, pt := range s.active(ss) {
		age := s.contactAge(pt)
		isHost := ss.Kind == "local" && pt.Role == "host"
		if isHost && age >= hostGoneMs {
			s.endSession(ss, ended{Reason: "host_gone", At: now})
			return
		}
		if !isHost && age >= participantTimeout {
			s.dropParticipant(ss, pt, "timeout")
			continue
		}
		if age >= graceMs && !pt.disconnectDone {
			pt.disconnectDone = true
			if s.releaseLocks(ss, pt) {
				ss.ver.Locks++
				changed = true
			}
			if pt.live != nil {
				pt.live = nil
				ss.ver.Live++
				changed = true
			}
		}
		if pt.live != nil && pt.live.Committed && now-pt.live.CommittedAt >= liveCommitKeepMs {
			pt.live = nil
			ss.ver.Live++
			changed = true
		}
	}
	if changed {
		s.notify(ss)
	}
	s.maybeEndHosted(ss)
	if ss.ended != nil {
		return
	}
	s.maybeAskCheckpoint(ss)

	kept := ss.previousSnaps[:0:0]
	for _, snap := range ss.previousSnaps {
		if snap.KeepUntil > now {
			kept = append(kept, snap)
		} else {
			_ = s.deps.Storage.DropSnapshot(ss.ID, snap.Seq)
			s.dirty(ss)
		}
	}
	ss.previousSnaps = kept

	if ss.snapshot != nil && ss.snapshot.Seq > ss.truncatedThrough {
		keepFrom := ss.snapshot.Seq
		for _, pt := range s.active(ss) {
			if s.contactAge(pt) < participantTimeout {
				keepFrom = min(keepFrom, s.keepFromCursor(ss, pt.Cursor))
			}
		}
		if ss.Kind == "hosted" {
			keepFrom = min(keepFrom, ss.hosted.SavedSeq)
		}
		if keepFrom > ss.truncatedThrough {
			if err := s.deps.Storage.Truncate(ss.ID, keepFrom); err != nil {
				s.logError("could not truncate the log", err)
				return
			}
			ss.truncatedThrough = keepFrom
			s.dirty(ss)
		}
	}
}

// keepFromCursor: a cursor inside a chain keeps the log from the chain's first link.
func (s *Service) keepFromCursor(ss *session, cursor int64) int64 {
	if e := s.entryAt(ss, cursor); e != nil && e.Chain != nil && e.Chain.Index < e.Chain.Count-1 {
		return cursor - int64(e.Chain.Index) - 1
	}
	return cursor
}

// maybeAskCheckpoint picks who uploads a checkpoint: the host of a local session; in a hosted session the
// participant in the list with the most recent contact (within 10 s), another one if he goes or loses contact,
// nobody while nobody has contact.
func (s *Service) maybeAskCheckpoint(ss *session) {
	if ss.ended != nil || ss.snapshot == nil {
		return
	}
	if ss.opsSinceSnap <= s.limits.CheckpointOps && ss.bytesSinceSnap <= s.limits.CheckpointBytes {
		return
	}
	cur := ss.participants[ss.checkpointFrom]
	if ss.Kind == "local" {
		if cur != nil && cur.Status == "active" {
			return
		}
		for _, pt := range s.visible(ss) {
			if pt.Role == "host" && s.contactAge(pt) < interruptedMs {
				ss.checkpointFrom = pt.ID
				pt.checkpointSent = false
				s.notify(ss)
			}
		}
		return
	}
	if cur != nil && cur.Status == "active" && cur.Visible && s.contactAge(cur) < interruptedMs {
		return
	}
	var candidates []*participant
	for _, pt := range s.visible(ss) {
		if s.contactAge(pt) < interruptedMs {
			candidates = append(candidates, pt)
		}
	}
	if len(candidates) == 0 {
		ss.checkpointFrom = ""
		return
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		ai, aj := s.contactAge(candidates[i]), s.contactAge(candidates[j])
		if ai != aj {
			return ai < aj
		}
		return candidates[i].lastContactEnd > candidates[j].lastContactEnd
	})
	chosen := candidates[0]
	if chosen.ID == ss.checkpointFrom {
		return
	}
	ss.checkpointFrom = chosen.ID
	chosen.checkpointSent = false
	s.notify(ss)
}

// EndIdle ends every session without a request of any participant for idleMs (reason server) — an operator's rule for
// sessions nobody comes back to. It names the sessions it ended.
func (s *Service) EndIdle(idleMs int64) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	var out []string
	for id, ss := range s.sessions {
		if ss.ended != nil || now-ss.lastActive < idleMs {
			continue
		}
		busy := false
		for _, pt := range ss.participants {
			busy = busy || pt.openRequests > 0
		}
		if !busy {
			s.endSession(ss, ended{Reason: "server", At: now})
			out = append(out, id)
		}
	}
	return out
}
