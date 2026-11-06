package core

import (
	"encoding/json"
	"strings"

	p "github.com/siemendev/dzstudio-collab-server/protocol"
)

func serialize(o *p.Obj) string { return p.Serialize(o) }

// record is the durable state of a session, written on structural changes (not on presence or live states).
type record struct {
	Version               int               `json:"version"`
	SessionID             string            `json:"sessionId"`
	Kind                  string            `json:"kind"`
	ProjectName           string            `json:"projectName"`
	Map                   string            `json:"map"`
	Requires              []Requirement     `json:"requires,omitempty"`
	Host                  *User             `json:"host,omitempty"`
	HostedRef             *HostedRef        `json:"hostedRef,omitempty"`
	CreatedAt             int64             `json:"createdAt"`
	CreatorID             string            `json:"creatorId"`
	LastActiveAt          int64             `json:"lastActiveAt,omitempty"`
	ExpectedSnapshotParts int               `json:"expectedSnapshotParts"`
	NextParticipantNo     int64             `json:"nextParticipantNo"`
	JoinCounter           int64             `json:"joinCounter"`
	Head                  int64             `json:"head"`
	NS                    int64             `json:"ns"`
	TruncatedThrough      int64             `json:"truncatedThrough"`
	Snapshot              *snapshotInfo     `json:"snapshot,omitempty"`
	PreviousSnapshots     []snapshotInfo    `json:"previousSnapshots,omitempty"`
	Uploads               []*upload         `json:"uploads,omitempty"`
	Attachments           []*attachment     `json:"attachments,omitempty"`
	AttachmentUploaders   map[string]string `json:"attachmentUploaders,omitempty"`
	Versions              versions          `json:"versions"`
	VersionCeiling        int64             `json:"versionCeiling"`
	Local                 localSaving       `json:"local"`
	Hosted                *hostedSaving     `json:"hosted,omitempty"`
	Ended                 *ended            `json:"ended,omitempty"`
	Participants          []*participant    `json:"participants"`
	Invites               []*invite         `json:"invites,omitempty"`
}

func (s *Service) recordOf(ss *session) []byte {
	r := record{
		Version: 1, SessionID: ss.ID, Kind: ss.Kind, ProjectName: ss.ProjectName, Map: ss.Map, Requires: ss.Requires,
		Host: ss.Host, HostedRef: ss.HostedRef, CreatedAt: ss.CreatedAt, CreatorID: ss.CreatorID, LastActiveAt: ss.lastActive,
		ExpectedSnapshotParts: ss.ExpectedSnapshotParts, NextParticipantNo: ss.nextPartNo, JoinCounter: ss.joinCounter,
		Head: ss.committed, NS: ss.ns, TruncatedThrough: ss.truncatedThrough, Snapshot: ss.snapshot,
		PreviousSnapshots: ss.previousSnaps, Versions: ss.ver, VersionCeiling: ss.verCeiling, Local: ss.local,
		Hosted: ss.hosted, Ended: ss.ended, Invites: s.sessionInvites(ss),
	}
	for _, up := range ss.uploads {
		r.Uploads = append(r.Uploads, up)
	}
	for _, a := range ss.attachments {
		r.Attachments = append(r.Attachments, a)
	}
	if len(ss.attachmentUploaders) > 0 {
		r.AttachmentUploaders = ss.attachmentUploaders
	}
	for _, pt := range ss.participants {
		r.Participants = append(r.Participants, pt)
	}
	ss.activeSaved = ss.lastActive
	data, err := json.Marshal(r)
	if err != nil {
		panic(err)
	}
	return data
}

// saveRecord writes the record now; s.mu is held.
func (s *Service) saveRecord(ss *session) {
	if err := s.deps.Storage.SaveRecord(ss.ID, s.recordOf(ss)); err != nil {
		ss.recordDirty = true
		s.logError("could not write the record of "+ss.ID, err)
		return
	}
	ss.recordDirty = false
}

func (s *Service) flushRecords() {
	for _, ss := range s.sessions {
		if ss.recordDirty {
			s.saveRecord(ss)
		}
	}
}

// reserveVersions makes sure every version reported so far stays below what a restart starts from.
func (s *Service) reserveVersions(ss *session) {
	m := max(ss.ver.Participants, ss.ver.Locks, ss.ver.Live, ss.ver.Saving)
	for _, pt := range ss.participants {
		m = max(m, pt.NoticeVer)
	}
	if m > ss.verCeiling {
		ss.verCeiling = m + versionReserve
		s.saveRecord(ss)
	}
}

// restoreSession rebuilds a session after a restart: durable fields from the record, the rest from the log.
func restoreSession(st StoredSession, now int64) (*session, []*invite, error) {
	var r record
	if err := json.Unmarshal(st.Record, &r); err != nil {
		return nil, nil, err
	}
	head := max(r.Head, r.TruncatedThrough)
	if r.Snapshot != nil {
		head = max(head, r.Snapshot.Seq)
	}
	ns := r.NS
	if n := len(st.Entries); n > 0 {
		head = max(head, st.Entries[n-1].Seq)
		ns = max(ns, st.Entries[n-1].NS)
	}
	base := max(r.VersionCeiling, r.Versions.Participants, r.Versions.Locks, r.Versions.Live, r.Versions.Saving)
	for _, pt := range r.Participants {
		base = max(base, pt.NoticeVer)
	}
	base++
	ss := &session{
		ID: r.SessionID, Kind: r.Kind, ProjectName: r.ProjectName, Map: r.Map, Requires: r.Requires, Host: r.Host,
		HostedRef: r.HostedRef, CreatedAt: r.CreatedAt, CreatorID: r.CreatorID, ExpectedSnapshotParts: r.ExpectedSnapshotParts,
		participants: map[string]*participant{}, nextPartNo: r.NextParticipantNo, joinCounter: r.JoinCounter,
		head: head, committed: head, ns: ns, truncatedThrough: r.TruncatedThrough, opIDs: map[string]int64{},
		snapshot: r.Snapshot, previousSnaps: r.PreviousSnapshots, uploads: map[int64]*upload{},
		attachments: map[string]*attachment{}, attachmentUploaders: map[string]string{}, wanted: map[string]int64{},
		ver: versions{base, base, base, base}, verCeiling: base, local: r.Local, hosted: r.Hosted,
		lockOwner: map[string]string{}, ended: r.Ended, wake: make(chan struct{}), recordDirty: true,
		lastActive: max(r.LastActiveAt, r.CreatedAt), activeSaved: max(r.LastActiveAt, r.CreatedAt),
	}
	for _, up := range r.Uploads {
		if up.Received == nil {
			up.Received = map[int]bool{}
		}
		ss.uploads[up.Seq] = up
	}
	for _, a := range r.Attachments {
		if !a.Complete && a.Received == nil {
			a.Received = map[int]bool{}
		}
		ss.attachments[a.Checksum] = a
	}
	for sum, pid := range r.AttachmentUploaders {
		ss.attachmentUploaders[sum] = pid
	}
	if ss.Kind == "hosted" && ss.hosted == nil {
		ss.hosted = &hostedSaving{}
	}
	maxLogged := map[string]int64{}
	for _, e := range st.Entries {
		ss.opIDs[opKey(e.OpID, e.Chain)] = e.Seq
		if ss.snapshot == nil || e.Seq > ss.snapshot.Seq {
			ss.opsSinceSnap++
		}
		maxLogged[e.ParticipantID] = max(maxLogged[e.ParticipantID], e.ClientSeq)
	}
	for _, pt := range r.Participants {
		pt.lastContactEnd = now
		pt.idleReportedAt = now
		pt.clientSeqToSeq = map[int64]int64{}
		pt.locks = map[string]struct{}{}
		pt.NoticeVer = base
		if pt.NextSeq < 1 {
			pt.NextSeq = 1
		}
		pt.NextSeq = max(pt.NextSeq, maxLogged[pt.ID]+1)
		ss.participants[pt.ID] = pt
	}
	for _, e := range st.Entries {
		if pt := ss.participants[e.ParticipantID]; pt != nil {
			pt.clientSeqToSeq[e.ClientSeq] = e.Seq
		}
	}
	for pid, items := range st.Staged {
		pt := ss.participants[pid]
		if pt == nil || pt.Status != "active" {
			continue
		}
		replayStaged(pt, items, maxLogged[pid])
	}
	for key, value := range st.Values {
		pid, ok := strings.CutPrefix(key, "locks-")
		pt := ss.participants[pid]
		if !ok || pt == nil {
			continue
		}
		var a lockAnswer
		if json.Unmarshal(value, &a) == nil && a.LockSeq >= pt.LockSeq {
			pt.LockSeq = a.LockSeq
			pt.lastLockAnswer = &a
		}
	}
	return ss, r.Invites, nil
}

// replayStaged rebuilds accepted, not yet appended parts and chain links.
func replayStaged(pt *participant, items []StagedItem, maxLogged int64) {
	for _, it := range items {
		if it.ClientSeq <= maxLogged {
			continue
		}
		pt.staged = append(pt.staged, it)
		parts := [][]string{it.Pieces}
		if it.Parts > 1 {
			pd := pt.pending
			if pd == nil || pd.ClientSeq != it.ClientSeq {
				pd = &pendingOp{ClientSeq: it.ClientSeq, OpID: it.OpID, System: it.System, LiveID: it.LiveID, Chain: it.Chain,
					Parts: make([][]string, it.Parts), Have: make([]bool, it.Parts)}
				pt.pending = pd
			}
			if it.Part < len(pd.Parts) && !pd.Have[it.Part] {
				pd.Parts[it.Part] = it.Pieces
				pd.Have[it.Part] = true
				pd.Bytes += p.PiecesBytes(it.Pieces)
			}
			for _, h := range pd.Have {
				if !h {
					parts = nil
				}
			}
			if parts == nil {
				continue
			}
			pt.pending = nil
			parts = pd.Parts
		}
		if it.Chain == nil {
			continue
		}
		if pt.chain == nil {
			pt.chain = &openChain{OpID: it.OpID, Count: it.Chain.Count, System: it.System, LiveID: it.LiveID}
		}
		pt.chain.Links = append(pt.chain.Links, chainLink{ClientSeq: it.ClientSeq, Parts: parts})
		pt.NextSeq = max(pt.NextSeq, it.ClientSeq+1)
	}
}
