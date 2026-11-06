package core

import (
	p "github.com/siemendev/dzstudio-collab-server/protocol"
)

func (s *Service) sessionInvites(ss *session) []*invite {
	out := []*invite{}
	for _, inv := range s.invites {
		if inv.SessionID == ss.ID {
			out = append(out, inv)
		}
	}
	return out
}

func (s *Service) hInviteCreate(r *p.R, _ RequestContext, u *User) *p.Obj {
	forUser, hasFor := r.OptID("forUserId")
	if hasFor && !s.opts.Capabilities.InviteUser {
		p.Fail(p.CapabilityMissing, "invite.user")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ss, pt := s.member(r, *u)
	if !pt.CanInvite {
		p.Fail(p.NotAllowed, "only participants with rights to the project invite")
	}
	code := NewCode()
	for s.invites[code] != nil {
		code = NewCode()
	}
	now := s.now()
	s.invites[code] = &invite{Code: code, SessionID: ss.ID, CreatedAt: now, ExpiresAt: now + inviteTTLMs, CreatedBy: pt.ID, ForUserID: forUser}
	s.dirty(ss)
	out := p.O("ok", true, "code", code, "expiresInSeconds", inviteTTLMs/1000)
	if hasFor {
		out.Set("forUserId", forUser)
	}
	return out
}

func (s *Service) hInviteRedeem(r *p.R, _ RequestContext, u *User) *p.Obj {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	var recent []int64
	for _, t := range s.redeemFails[u.UserID] {
		if now-t < 60_000 {
			recent = append(recent, t)
		}
	}
	s.redeemFails[u.UserID] = recent
	if len(recent) >= redeemFailures {
		p.Fail(p.Throttled, "too many attempts", "retryAfterMs", max(1, 60_000-(now-recent[0])))
	}
	code := NormalizeCode(r.String("code", 64, 0))
	failed := func(c string) {
		s.redeemFails[u.UserID] = append(s.redeemFails[u.UserID], now)
		p.Fail(c, "")
	}
	inv := s.invites[code]
	if inv == nil {
		failed(p.InviteInvalid)
	}
	ss := s.sessions[inv.SessionID]
	if ss == nil {
		failed(p.InviteInvalid)
	}
	if ss.ended != nil {
		failEnded(ss.ended)
	}
	if (inv.RedeemedBy != "" && inv.RedeemedBy != u.UserID) || (inv.ForUserID != "" && inv.ForUserID != u.UserID) || inv.Invalid {
		failed(p.InviteUsed)
	}
	if now >= inv.ExpiresAt {
		failed(p.InviteExpired)
	}
	inv.RedeemedBy = u.UserID
	s.dirty(ss)
	out := p.O("ok", true, "sessionId", ss.ID, "kind", ss.Kind, "projectName", ss.ProjectName, "map", ss.Map)
	if ss.Kind == "local" {
		out.Set("host", userObj(*ss.Host))
	}
	if ss.Requires != nil {
		out.Set("requires", requiresView(ss.Requires))
	}
	return out
}

func (s *Service) hParticipantRemove(r *p.R, _ RequestContext, u *User) *p.Obj {
	target := r.ID("target")
	s.mu.Lock()
	defer s.mu.Unlock()
	ss, pt := s.member(r, *u)
	t := ss.participants[target]
	own := s.opts.Capabilities.Agents && ownAgent(pt, t)
	if !pt.CanManage && !own {
		p.Fail(p.NotAllowed, "only who manages the session removes participants")
	}
	if target == pt.ID {
		p.BadRequestf("nobody removes themselves")
	}
	if t == nil {
		p.BadRequestf("unknown target")
	}
	if ss.Kind == "local" && t.Role == "host" {
		p.BadRequestf("nobody removes the host")
	}
	if t.Status != "active" {
		return p.O("ok", true)
	}
	t.Status = "removed"
	t.RemovedBy = pt.User.Name
	locks := s.releaseLocks(ss, t)
	live := t.live != nil
	t.live = nil
	s.dropUnappended(ss, t)
	for _, inv := range s.sessionInvites(ss) {
		if !own && (inv.RedeemedBy == t.User.UserID || inv.ForUserID == t.User.UserID) {
			inv.Invalid = true
		}
	}
	for _, other := range s.active(ss) {
		s.pushNotice(other, notice{Kind: "participant_removed", ParticipantID: t.ID, Name: t.User.Name, By: pt.User.Name})
	}
	if t.Visible {
		ss.ver.Participants++
	}
	if locks {
		ss.ver.Locks++
	}
	if live {
		ss.ver.Live++
	}
	if ss.checkpointFrom == t.ID {
		ss.checkpointFrom = ""
	}
	s.cutPoll(t)
	s.dirty(ss)
	s.notify(ss)
	return p.O("ok", true)
}

func (s *Service) cutPoll(pt *participant) {
	if pt.cut != nil {
		close(pt.cut)
		pt.cut = nil
	}
}

func (s *Service) hSessionLeave(r *p.R, _ RequestContext, u *User) *p.Obj {
	sid := r.ID("sessionId")
	pid := r.ID("participantId")
	s.mu.Lock()
	defer s.mu.Unlock()
	ss := s.sessions[sid]
	if ss == nil {
		return p.O("ok", true)
	}
	pt := ss.participants[pid]
	if pt == nil || pt.User.UserID != u.UserID || pt.Status != "active" || ss.ended != nil {
		return p.O("ok", true)
	}
	s.dropParticipant(ss, pt, "left")
	if ss.Kind == "local" && pt.Role == "host" {
		s.endSession(ss, ended{Reason: "host_left", At: s.now()})
	} else {
		s.maybeEndHosted(ss)
	}
	return p.O("ok", true)
}

func (s *Service) dropParticipant(ss *session, pt *participant, status string) {
	pt.Status = status
	locks := s.releaseLocks(ss, pt)
	live := pt.live != nil
	pt.live = nil
	s.dropUnappended(ss, pt)
	s.dropPartialAttachments(ss, pt)
	if pt.Visible {
		ss.ver.Participants++
	}
	if locks {
		ss.ver.Locks++
	}
	if live {
		ss.ver.Live++
	}
	if ss.checkpointFrom == pt.ID {
		ss.checkpointFrom = ""
	}
	s.cutPoll(pt)
	s.dirty(ss)
	s.notify(ss)
}

func (s *Service) maybeEndHosted(ss *session) {
	if ss.Kind != "hosted" || ss.ended != nil || ss.snapshot == nil {
		return
	}
	if len(s.active(ss)) == 0 && s.unsavedCount(ss) == 0 {
		s.endSession(ss, ended{Reason: "server", At: s.now()})
	}
}

func (s *Service) hSessionEnd(r *p.R, _ RequestContext, u *User) *p.Obj {
	sid := r.ID("sessionId")
	pid := r.ID("participantId")
	s.mu.Lock()
	defer s.mu.Unlock()
	ss := s.sessions[sid]
	if ss == nil || ss.ended != nil {
		return p.O("ok", true)
	}
	pt := ss.participants[pid]
	if pt == nil || pt.User.UserID != u.UserID {
		failNotParticipant(nil)
	}
	if pt.Status != "active" {
		failNotParticipant(pt)
	}
	if ss.Kind == "local" {
		if pt.Role != "host" {
			p.Fail(p.NotAllowed, "only the host ends the session")
		}
		s.endSession(ss, ended{Reason: "host_ended", At: s.now()})
	} else {
		if !pt.CanManage {
			p.Fail(p.NotAllowed, "only who manages the project discards")
		}
		s.endSession(ss, ended{Reason: "discarded", By: pt.User.Name, At: s.now()})
	}
	return p.O("ok", true)
}

func (s *Service) endSession(ss *session, e ended) {
	if ss.ended != nil {
		return
	}
	ss.ended = &e
	for _, pt := range ss.participants {
		pt.locks = map[string]struct{}{}
		pt.live = nil
		s.dropUnappended(ss, pt)
		s.cutPoll(pt)
	}
	ss.lockOwner = map[string]string{}
	ss.lockCount = 0
	ss.uploads = map[int64]*upload{}
	s.dropAttachments(ss)
	ss.checkpointFrom = ""
	s.logInfo("session %s ended (%s)", ss.ID, e.Reason)
	s.notify(ss)
	if err := s.deps.Storage.Truncate(ss.ID, ss.head); err != nil {
		s.logError("could not drop the log of an ended session", err)
	}
	if ss.snapshot != nil {
		_ = s.deps.Storage.DropSnapshot(ss.ID, ss.snapshot.Seq)
	}
	for _, snap := range ss.previousSnaps {
		_ = s.deps.Storage.DropSnapshot(ss.ID, snap.Seq)
	}
	ss.snapshot = nil
	ss.previousSnaps = nil
	ss.truncatedThrough = ss.head
	s.saveRecord(ss)
	if ss.HostedRef != nil && s.deps.Hooks != nil {
		go s.deps.Hooks.HostedEnded(ss.ID, *ss.HostedRef)
	}
}

// HostedSession names a running hosted session.
type HostedSession struct {
	SessionID string
	Ref       HostedRef
	// Creator started the session (dzbot marks its hold in this user's name).
	Creator User
	// Revision is the stand of the session's last save, at first the one it started from.
	Revision string
}

// HostedSessions lists the hosted sessions that have not ended (also those restored after a restart).
func (s *Service) HostedSessions() []HostedSession {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []HostedSession
	for _, ss := range s.sessions {
		if ss.ended == nil && ss.HostedRef != nil {
			h := HostedSession{SessionID: ss.ID, Ref: *ss.HostedRef}
			if creator := ss.participants[ss.CreatorID]; creator != nil {
				h.Creator = creator.User
			}
			if ss.hosted != nil {
				h.Revision = ss.hosted.BaseRevision
			}
			out = append(out, h)
		}
	}
	return out
}

// EndByOperator ends a session from the operator side (reason server).
func (s *Service) EndByOperator(sessionID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	ss := s.sessions[sessionID]
	if ss == nil || ss.ended != nil {
		return false
	}
	s.endSession(ss, ended{Reason: "server", At: s.now()})
	return true
}

// SessionCount is the number of sessions that have not ended.
func (s *Service) SessionCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, ss := range s.sessions {
		if ss.ended == nil {
			n++
		}
	}
	return n
}
