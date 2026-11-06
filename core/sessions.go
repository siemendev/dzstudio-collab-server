package core

import (
	"fmt"
	"slices"
	"sort"

	p "github.com/siemendev/dzstudio-collab-server/protocol"
)

// ------------------------------------------------------------ helpers (all with s.mu held)

func (s *Service) session(id string) *session {
	ss := s.sessions[id]
	if ss == nil {
		p.Fail(p.SessionUnknown, "")
	}
	return ss
}

func (s *Service) running(id string) *session {
	ss := s.session(id)
	if ss.ended != nil {
		failEnded(ss.ended)
	}
	return ss
}

func failEnded(e *ended) {
	if e.By != "" {
		p.Fail(p.SessionEnded, "", "reason", e.Reason, "by", e.By)
	}
	p.Fail(p.SessionEnded, "", "reason", e.Reason)
}

func failNotParticipant(pt *participant) {
	if pt != nil && pt.Status == "removed" {
		p.Fail(p.NotParticipant, "", "reason", "removed", "by", pt.RemovedBy)
	}
	p.Fail(p.NotParticipant, "", "reason", "timeout")
}

// member is the participant behind (sessionId, participantId) for this user, still in the session.
func (s *Service) member(r *p.R, u User) (*session, *participant) {
	sid := r.ID("sessionId")
	pid := r.ID("participantId")
	ss := s.session(sid)
	if ss.ended != nil {
		failEnded(ss.ended)
	}
	pt := ss.participants[pid]
	if pt == nil || pt.User.UserID != u.UserID {
		failNotParticipant(nil)
	}
	if pt.Status != "active" {
		failNotParticipant(pt)
	}
	return ss, pt
}

func (s *Service) project(ss *session) ProjectContext {
	if ss.Kind == "local" {
		return ProjectContext{Kind: "local", Host: *ss.Host, ProjectName: ss.ProjectName, Map: ss.Map}
	}
	return ProjectContext{Kind: "hosted", HostedRef: *ss.HostedRef, ProjectName: ss.ProjectName, Map: ss.Map}
}

func (s *Service) rightsFor(ss *session, u User) ProjectRights {
	if ss.Kind == "local" && ss.Host.UserID == u.UserID {
		return ProjectRights{Join: true, Manage: true, Invite: true}
	}
	r := must(s.deps.Permissions.Rights(u, s.project(ss)))
	if ss.Kind == "local" {
		return ProjectRights{Join: r.Join, Invite: r.Join && r.Invite}
	}
	return r
}

func (s *Service) validInviteFor(ss *session, u User) *invite {
	now := s.now()
	for _, inv := range s.invites {
		if inv.SessionID != ss.ID || inv.Invalid || now >= inv.ExpiresAt {
			continue
		}
		if inv.RedeemedBy == u.UserID || (inv.ForUserID == u.UserID && inv.RedeemedBy == "") {
			return inv
		}
	}
	return nil
}

func (s *Service) sessionCaps(ss *session) []string {
	caps := []string{}
	if s.opts.Capabilities.Users && s.deps.Directory != nil {
		caps = append(caps, "users")
	}
	if s.opts.Capabilities.InviteUser {
		caps = append(caps, "invite.user")
	}
	if ss.Kind == "hosted" {
		caps = append(caps, "hosted")
	}
	return caps
}

func (s *Service) active(ss *session) []*participant {
	out := []*participant{}
	for _, pt := range ss.participants {
		if pt.Status == "active" {
			out = append(out, pt)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].JoinOrder < out[j].JoinOrder })
	return out
}

func (s *Service) visible(ss *session) []*participant {
	out := []*participant{}
	for _, pt := range s.active(ss) {
		if pt.Visible {
			out = append(out, pt)
		}
	}
	return out
}

func (s *Service) pickColour(ss *session) int {
	counts := make([]int, 9)
	for _, pt := range s.active(ss) {
		counts[pt.Colour]++
	}
	for c := 1; c <= 8; c++ {
		if counts[c] == 0 {
			return c
		}
	}
	best := 1
	for c := 2; c <= 8; c++ {
		if counts[c] < counts[best] {
			best = c
		}
	}
	return best
}

func (s *Service) notify(ss *session) {
	close(ss.wake)
	ss.wake = make(chan struct{})
}

func (s *Service) dirty(ss *session) { ss.recordDirty = true }

func (s *Service) pushNotice(pt *participant, n notice) {
	pt.NoticeVer++
	pt.notices = append(pt.notices, numberedNotice{pt.NoticeVer, n})
	if len(pt.notices) > maxNotices {
		pt.notices = pt.notices[len(pt.notices)-maxNotices:]
	}
}

func (s *Service) readClient(r *p.R, ctx RequestContext) clientInfo {
	c := r.Object("client")
	name := c.String("name", 64, 0)
	c.String("version", 64, 0)
	if len(s.opts.AllowedClients) > 0 && !slices.Contains(s.opts.AllowedClients, name) {
		p.BadRequestf("client %s is not accepted by this server", name)
	}
	kind := s.clientKind(c)
	if !ctx.agentToken && kind != "agent" {
		return clientInfo{}
	}
	return clientInfo{agent: true, agentName: s.readAgentName(c)}
}

func readRequires(r *p.R) []Requirement {
	list, ok := r.OptArray("requires", 1000)
	if !ok {
		return nil
	}
	out := make([]Requirement, len(list))
	for i, item := range list {
		ir := p.Item(item, "requires")
		out[i] = Requirement{Type: ir.String("type", 200, 0), Version: ir.Int("version", 0, p.MaxInt)}
	}
	return out
}

func readHostedRef(r *p.R) HostedRef {
	ref := r.Object("hostedRef")
	return HostedRef{Provider: ref.String("provider", 200, 1), Key: ref.String("key", 200, 1)}
}

func (s *Service) store(provider string) ProjectStore {
	for _, st := range s.deps.ProjectStores {
		if st.Provider() == provider {
			return st
		}
	}
	return nil
}

func (s *Service) newParticipant(ss *session, u User, role string, canInvite, canSave, canManage, viaInvite bool, key string) *participant {
	now := s.now()
	pt := &participant{
		ID:             "p" + itoa(ss.nextPartNo),
		User:           u,
		Role:           role,
		Colour:         s.pickColour(ss),
		CanInvite:      canInvite,
		CanSave:        canSave,
		CanManage:      canManage,
		ViaInvite:      viaInvite,
		InstanceKey:    key,
		JoinOrder:      ss.joinCounter,
		Status:         "active",
		NextSeq:        1,
		lastContactEnd: now,
		idleReportedAt: now,
		clientSeqToSeq: map[int64]int64{},
		locks:          map[string]struct{}{},
	}
	ss.nextPartNo++
	ss.joinCounter++
	ss.participants[pt.ID] = pt
	return pt
}

func (s *Service) participantView(pt *participant) *p.Obj {
	o := p.O("participantId", pt.ID, "userId", pt.User.UserID, "name", pt.User.Name, "colour", pt.Colour,
		"role", pt.Role, "canInvite", pt.CanInvite, "canSave", pt.CanSave, "canManage", pt.CanManage)
	if s.opts.Capabilities.Agents {
		if pt.isAgent() {
			o.Set("kind", "agent")
			if pt.AgentName != "" {
				o.Set("agentName", pt.AgentName)
			}
		} else {
			o.Set("kind", "studio")
		}
	}
	return o
}

// markAgent makes pt an agent: never saving, managing or inviting, whatever its user may do.
func (s *Service) markAgent(pt *participant, c clientInfo) {
	if !c.agent {
		return
	}
	pt.Kind = "agent"
	pt.AgentName = c.agentName
	pt.CanInvite, pt.CanSave, pt.CanManage = false, false, false
}

func userObj(u User) *p.Obj { return p.O("userId", u.UserID, "name", u.Name) }

func refObj(r HostedRef) *p.Obj { return p.O("provider", r.Provider, "key", r.Key) }

func requiresView(reqs []Requirement) []*p.Obj {
	out := make([]*p.Obj, len(reqs))
	for i, r := range reqs {
		out[i] = p.O("type", r.Type, "version", r.Version)
	}
	return out
}

// runningHosted is the session of a hosted project that has not ended, if any.
func (s *Service) runningHosted(ref HostedRef) *session {
	for _, ss := range s.sessions {
		if ss.ended == nil && ss.HostedRef != nil && *ss.HostedRef == ref {
			return ss
		}
	}
	return nil
}

// ------------------------------------------------------------ lists

func (s *Service) hSessionsList(r *p.R, _ RequestContext, u *User) *p.Obj {
	var filter *HostedRef
	if r.Has("hostedRef") {
		if !s.opts.Capabilities.Hosted {
			p.Fail(p.CapabilityMissing, "hosted")
		}
		ref := readHostedRef(r)
		filter = &ref
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := make([]string, 0, len(s.sessions))
	for id := range s.sessions {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return s.sessions[ids[i]].CreatedAt < s.sessions[ids[j]].CreatedAt })
	out := []*p.Obj{}
	for _, id := range ids {
		ss := s.sessions[id]
		if ss.ended != nil || ss.snapshot == nil {
			continue
		}
		if filter != nil && (ss.HostedRef == nil || *ss.HostedRef != *filter) {
			continue
		}
		access := s.accessOf(ss, *u)
		if access == "" {
			continue
		}
		out = append(out, s.sessionSummary(ss, access))
		if len(out) >= 200 {
			break
		}
	}
	return p.O("ok", true, "sessions", out)
}

func (s *Service) accessOf(ss *session, u User) string {
	if s.rightsFor(ss, u).Join {
		return "direct"
	}
	if s.validInviteFor(ss, u) != nil {
		return "invited"
	}
	return ""
}

func (s *Service) sessionSummary(ss *session, access string) *p.Obj {
	o := p.O("sessionId", ss.ID, "kind", ss.Kind, "projectName", ss.ProjectName, "map", ss.Map)
	if ss.Kind == "local" {
		o.Set("host", userObj(*ss.Host))
	}
	o.Set("participants", len(s.visible(ss)))
	if ss.Kind == "hosted" {
		o.Set("unsavedCount", s.unsavedCount(ss))
		o.Set("hostedRef", refObj(*ss.HostedRef))
	}
	o.Set("access", access)
	if ss.Requires != nil {
		o.Set("requires", requiresView(ss.Requires))
	}
	return o
}

func (s *Service) hUsersList(_ *p.R, _ RequestContext, u *User) *p.Obj {
	users := must(s.deps.Directory.ListAvailable(*u))
	if len(users) > 200 {
		users = users[:200]
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	inSession := map[string]bool{}
	for _, ss := range s.sessions {
		if ss.ended != nil {
			continue
		}
		for _, pt := range ss.participants {
			if pt.Status == "active" && pt.Visible && s.contactAge(pt) < graceMs {
				inSession[pt.User.UserID] = true
			}
		}
	}
	out := []*p.Obj{}
	for _, x := range users {
		state := "offline"
		if inSession[x.UserID] {
			state = "in_session"
		} else if seen, ok := s.userSeen[x.UserID]; ok && now-seen.at < onlineMs {
			state = "online"
		}
		out = append(out, p.O("userId", x.UserID, "name", p.TruncateUTF8(x.Name, 64), "state", state))
	}
	return p.O("ok", true, "users", out)
}

// ------------------------------------------------------------ create, snapshot, join

// checkStart refuses a new session: another one runs for the project, or the user already has as many sessions of
// this kind as the server allows; s.mu is held.
func (s *Service) checkStart(kind string, ref *HostedRef, u User) {
	if ref != nil {
		if other := s.runningHosted(*ref); other != nil {
			p.Fail(p.AlreadyRunning, "", "sessionId", other.ID)
		}
	}
	limit := s.limits.MaxLocalSessionsPerUser
	if kind == "hosted" {
		limit = s.limits.MaxHostedSessionsPerUser
	}
	if limit <= 0 {
		return
	}
	n := 0
	for _, ss := range s.sessions {
		if creator := ss.participants[ss.CreatorID]; ss.ended == nil && ss.Kind == kind && creator != nil && creator.User.UserID == u.UserID {
			n++
		}
	}
	if n >= limit {
		p.Fail(p.NotAllowed, fmt.Sprintf("You already started %d sessions of this kind; end one of them first.", n))
	}
}

func (s *Service) hSessionCreate(r *p.R, ctx RequestContext, u *User) *p.Obj {
	client := s.readClient(r, ctx)
	projectName := r.String("projectName", 200, 0)
	mapKey := r.String("map", 64, 0)
	kind := r.OneOf("kind", "local", "hosted")
	if client.agent && kind == "local" {
		p.Fail(p.NotAllowed, "an agent never hosts a session")
	}
	requires := readRequires(r)
	key := r.InstanceKey()
	snapshotBytes := r.Int("snapshotBytes", 0, p.MaxInt)
	snapshotParts := r.Int("snapshotParts", 1, p.MaxInt)
	var ref *HostedRef
	revision := ""
	var rights ProjectRights
	if kind == "hosted" {
		if !s.opts.Capabilities.Hosted {
			p.Fail(p.CapabilityMissing, "hosted")
		}
		hr := readHostedRef(r)
		ref = &hr
		revision = r.String("hostedRevision", 200, 0)
		if client.agent && revision == "" {
			p.Fail(p.NotAllowed, "an agent does not create projects; a person creates the project first")
		}
		rights = must(s.deps.Permissions.Rights(*u, ProjectContext{Kind: "hosted", HostedRef: hr, ProjectName: projectName, Map: mapKey}))
		if !rights.Save {
			p.Fail(p.NotAllowed, "only who may save this project starts a session")
		}
		if s.store(hr.Provider) == nil {
			p.Fail(p.NotAllowed, "this server has no project store \""+hr.Provider+"\"")
		}
	} else {
		if !must(s.deps.Permissions.MayHostLocal(*u)) {
			p.Fail(p.NotAllowed, "you may not start sessions here")
		}
		rights = ProjectRights{Join: true, Manage: true, Invite: true}
	}
	if snapshotBytes > s.limits.MaxSnapshotBytes {
		p.Fail(p.TooLarge, "project state too large for this server", "maxBytes", s.limits.MaxSnapshotBytes)
	}

	sessionID := newID("s")
	if ref != nil && s.deps.Hooks != nil {
		func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			s.checkStart(kind, ref, *u)
		}()
		if err := s.deps.Hooks.HostedStarting(sessionID, *ref, revision, *u); err != nil {
			must(0, err)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if failure := catch(func() { s.checkStart(kind, ref, *u) }); failure != nil {
		if ref != nil && s.deps.Hooks != nil {
			go s.deps.Hooks.HostedEnded(sessionID, *ref)
		}
		panic(failure)
	}
	now := s.now()
	ss := &session{
		ID:                    sessionID,
		Kind:                  kind,
		ProjectName:           projectName,
		Map:                   mapKey,
		Requires:              requires,
		HostedRef:             ref,
		CreatedAt:             now,
		ExpectedSnapshotParts: int(snapshotParts),
		participants:          map[string]*participant{},
		nextPartNo:            1,
		opIDs:                 map[string]int64{},
		uploads:               map[int64]*upload{},
		attachments:           map[string]*attachment{},
		attachmentUploaders:   map[string]string{},
		wanted:                map[string]int64{},
		ver:                   versions{1, 1, 1, 1},
		local:                 localSaving{State: "none"},
		lockOwner:             map[string]string{},
		wake:                  make(chan struct{}),
		recordDirty:           true,
		lastActive:            now,
		activeSaved:           now,
	}
	if kind == "local" {
		host := *u
		ss.Host = &host
	} else {
		ss.hosted = &hostedSaving{BaseRevision: revision, CreatedRevision: revision}
	}
	role := "participant"
	if kind == "local" {
		role = "host"
	}
	pt := s.newParticipant(ss, *u, role, rights.Invite || kind == "local", kind == "hosted" && rights.Save,
		kind == "local" || rights.Manage, false, key)
	s.markAgent(pt, client)
	ss.CreatorID = pt.ID
	s.sessions[ss.ID] = ss
	s.saveRecord(ss)
	s.logInfo("session %s (%s) created by %s", ss.ID, kind, u.UserID)
	return p.O("ok", true, "sessionId", ss.ID, "participantId", pt.ID, "role", pt.Role, "colour", pt.Colour,
		"canInvite", pt.CanInvite, "canSave", pt.CanSave, "canManage", pt.CanManage, "capabilities", s.sessionCaps(ss))
}

func (s *Service) hSnapshotPut(r *p.R, _ RequestContext, u *User) *p.Obj {
	seq := r.Int("seq", 0, p.MaxInt)
	parts := int(r.Int("parts", 1, 100_000))
	part := int(r.Int("part", 0, int64(parts-1)))
	pieces, bytes := p.Pieces(r.M["pieces"], "pieces")
	if bytes > PartBytes {
		p.Fail(p.TooLarge, "part over 1 MB", "maxBytes", PartBytes)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ss, pt := s.member(r, *u)
	if ss.snapshot != nil && seq == ss.snapshot.Seq {
		return p.O("ok", true, "complete", true)
	}
	if seq == 0 {
		if pt.ID != ss.CreatorID {
			p.Fail(p.NotAllowed, "only the creator uploads the first snapshot")
		}
		if parts != ss.ExpectedSnapshotParts {
			p.BadRequestf("parts differs from snapshotParts")
		}
	} else {
		if ss.checkpointFrom != pt.ID {
			p.Fail(p.NotAllowed, "no checkpoint was asked from you")
		}
		if ss.snapshot == nil || seq <= ss.snapshot.Seq || seq > ss.committed {
			p.BadRequestf("checkpoint seq out of range")
		}
		if s.insideChain(ss, seq) {
			p.BadRequestf("seq lies inside a chain")
		}
	}
	up := ss.uploads[seq]
	if up != nil && up.Parts != parts {
		p.BadRequestf("parts differs from earlier parts of this snapshot")
	}
	if up == nil {
		up = &upload{Seq: seq, Parts: parts, Received: map[int]bool{}, By: pt.ID}
		ss.uploads[seq] = up
	}
	if !up.Received[part] {
		if up.Bytes+int64(bytes) > s.limits.MaxSnapshotBytes {
			p.Fail(p.TooLarge, "project state too large for this server", "maxBytes", s.limits.MaxSnapshotBytes)
		}
		must(0, s.deps.Storage.PutSnapshotPart(ss.ID, SnapshotPart{Seq: seq, Part: part, Parts: parts, Pieces: pieces}))
		up.Received[part] = true
		up.Bytes += int64(bytes)
		s.dirty(ss)
	}
	complete := len(up.Received) == up.Parts
	if complete {
		s.completeSnapshot(ss, up)
	}
	return p.O("ok", true, "complete", complete)
}

func (s *Service) completeSnapshot(ss *session, up *upload) {
	delete(ss.uploads, up.Seq)
	ns := s.nsAt(ss, up.Seq)
	if ss.snapshot != nil {
		prev := *ss.snapshot
		prev.KeepUntil = s.now() + snapshotKeepMs
		ss.previousSnaps = append(ss.previousSnaps, prev)
	}
	ss.snapshot = &snapshotInfo{Seq: up.Seq, Parts: up.Parts, Bytes: up.Bytes, NS: ns}
	for seq := range ss.uploads {
		if seq <= up.Seq {
			delete(ss.uploads, seq)
			_ = s.deps.Storage.DropSnapshot(ss.ID, seq)
		}
	}
	if up.Seq > 0 {
		ss.checkpointFrom = ""
		for _, pt := range ss.participants {
			pt.checkpointSent = false
		}
		ss.opsSinceSnap = ss.head - up.Seq
		ss.bytesSinceSnap = 0
	}
	s.dirty(ss)
	s.notify(ss)
}

// entryAt reads the log entry with this seq, if it is still kept.
func (s *Service) entryAt(ss *session, seq int64) *LogEntry {
	if seq <= 0 {
		return nil
	}
	entries, err := s.deps.Storage.Read(ss.ID, seq-1, 1)
	if err != nil || len(entries) == 0 || entries[0].Seq != seq {
		return nil
	}
	return &entries[0]
}

func (s *Service) nsAt(ss *session, seq int64) int64 {
	if seq == 0 {
		return 0
	}
	if ss.snapshot != nil && seq == ss.snapshot.Seq {
		return ss.snapshot.NS
	}
	if e := s.entryAt(ss, seq); e != nil {
		return e.NS
	}
	if ss.snapshot != nil {
		return ss.snapshot.NS
	}
	return 0
}

// insideChain: seq is a link of a chain other than its last.
func (s *Service) insideChain(ss *session, seq int64) bool {
	e := s.entryAt(ss, seq)
	return e != nil && e.Chain != nil && e.Chain.Index < e.Chain.Count-1
}

func (s *Service) hSnapshotGet(r *p.R, _ RequestContext, u *User) *p.Obj {
	seq := r.Int("seq", 0, p.MaxInt)
	part := int(r.Int("part", 0, p.MaxInt))
	s.mu.Lock()
	defer s.mu.Unlock()
	ss, _ := s.member(r, *u)
	now := s.now()
	var info *snapshotInfo
	if ss.snapshot != nil && ss.snapshot.Seq == seq {
		info = ss.snapshot
	} else {
		for i := range ss.previousSnaps {
			if ss.previousSnaps[i].Seq == seq && ss.previousSnaps[i].KeepUntil > now {
				info = &ss.previousSnaps[i]
			}
		}
	}
	if info == nil {
		if ss.snapshot == nil && seq == 0 {
			p.Fail(p.SnapshotNotReady, "")
		}
		p.Fail(p.SnapshotUnknown, "")
	}
	if part >= info.Parts {
		p.BadRequestf("part out of range")
	}
	stored, err := s.deps.Storage.GetSnapshotPart(ss.ID, seq, part)
	if err != nil || stored == nil {
		p.Fail(p.SnapshotUnknown, "")
	}
	return p.O("ok", true, "seq", seq, "part", part, "parts", info.Parts, "pieces", stored.Pieces)
}

func (s *Service) hSessionJoin(r *p.R, ctx RequestContext, u *User) *p.Obj {
	client := s.readClient(r, ctx)
	sid := r.ID("sessionId")
	key := r.InstanceKey()
	resume := r.OptObject("resume")
	var resumeID string
	var after int64
	if resume != nil {
		resumeID = resume.ID("participantId")
		after = resume.Int("after", 0, p.MaxInt)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ss := s.running(sid)
	if resume != nil {
		pt := ss.participants[resumeID]
		if pt == nil || pt.User.UserID != u.UserID {
			failNotParticipant(nil)
		}
		if pt.Status != "active" {
			failNotParticipant(pt)
		}
		if after > ss.committed {
			p.BadRequestf("resume.after is beyond the highest seq")
		}
		if ss.snapshot == nil {
			p.Fail(p.SnapshotNotReady, "")
		}
		mode := "snapshot"
		if after >= ss.truncatedThrough {
			mode = "resume"
		}
		pt.Cursor = after
		return s.joinAnswer(ss, pt, mode)
	}
	if ss.snapshot == nil {
		p.Fail(p.SnapshotNotReady, "")
	}
	rights := s.rightsFor(ss, *u)
	var inv *invite
	if !rights.Join {
		inv = s.validInviteFor(ss, *u)
		if inv == nil {
			p.Fail(p.NotAllowed, "you have no right to join this session")
		}
	}
	act := s.active(ss)
	for _, x := range act {
		if x.InstanceKey == key {
			p.Fail(p.InstanceKeyTaken, "")
		}
	}
	if len(act) >= s.limits.MaxParticipants {
		p.Fail(p.SessionFull, "")
	}
	local := ss.Kind == "local"
	role := "participant"
	if local {
		role = "guest"
	}
	pt := s.newParticipant(ss, *u, role, rights.Join && rights.Invite, !local && rights.Save, !local && rights.Manage, !rights.Join, key)
	s.markAgent(pt, client)
	if inv != nil && inv.ForUserID == u.UserID && inv.RedeemedBy == "" {
		inv.RedeemedBy = u.UserID
	}
	pt.Cursor = ss.snapshot.Seq
	s.dirty(ss)
	return s.joinAnswer(ss, pt, "snapshot")
}

func (s *Service) joinAnswer(ss *session, pt *participant, mode string) *p.Obj {
	o := p.O("ok", true, "participantId", pt.ID, "colour", pt.Colour, "kind", ss.Kind, "role", pt.Role,
		"canInvite", pt.CanInvite, "canSave", pt.CanSave, "canManage", pt.CanManage, "mode", mode)
	if mode == "snapshot" {
		o.Set("snapshot", p.O("seq", ss.snapshot.Seq, "parts", ss.snapshot.Parts, "bytes", ss.snapshot.Bytes))
	}
	o.Set("seq", ss.committed)
	o.Set("capabilities", s.sessionCaps(ss))
	return o
}
