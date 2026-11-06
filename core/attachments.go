package core

import (
	"sync"

	p "github.com/siemendev/dzstudio-collab-server/protocol"
)

// MaxAttachmentBytes is fixed in 1.0.
const MaxAttachmentBytes = 16_777_216

const wantGapMs = 10_000

// attachment is the bookkeeping of one attachment of a session; its parts live in AttachmentStorage.
type attachment struct {
	Checksum string       `json:"checksum"`
	Bytes    int64        `json:"bytes"`
	Parts    int          `json:"parts"`
	Seq      int64        `json:"seq"`
	By       string       `json:"by"`
	Received map[int]bool `json:"received,omitempty"`
	Complete bool         `json:"complete"`
	At       int64        `json:"at"`
	got      int64
}

// memAttachments is the AttachmentStorage the core uses when none is given: in memory, lost on a restart.
type memAttachments struct {
	mu    sync.Mutex
	parts map[string]map[string]map[int][]string
}

func newMemAttachments() *memAttachments {
	return &memAttachments{parts: map[string]map[string]map[int][]string{}}
}

func (m *memAttachments) PutPart(sessionID, checksum string, part int, pieces []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	bySum := m.parts[sessionID]
	if bySum == nil {
		bySum = map[string]map[int][]string{}
		m.parts[sessionID] = bySum
	}
	if bySum[checksum] == nil {
		bySum[checksum] = map[int][]string{}
	}
	bySum[checksum][part] = append([]string{}, pieces...)
	return nil
}

func (m *memAttachments) GetPart(sessionID, checksum string, part int) ([]string, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	pieces, ok := m.parts[sessionID][checksum][part]
	return pieces, ok, nil
}

func (m *memAttachments) Drop(sessionID, checksum string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.parts[sessionID], checksum)
	return nil
}

func (m *memAttachments) DropSession(sessionID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.parts, sessionID)
	return nil
}

func (s *Service) hAttachmentPut(r *p.R, _ RequestContext, u *User) *p.Obj {
	s.mu.Lock()
	defer s.mu.Unlock()
	ss, pt := s.member(r, *u)
	checksum := r.String("checksum", 200, 1)
	seq := r.Int("seq", 0, p.MaxInt)
	bytes := r.Int("bytes", 0, p.MaxInt)
	parts := int(r.Int("parts", 1, 100_000))
	part := int(r.Int("part", 0, int64(parts-1)))
	pieces, wire := p.Pieces(r.M["pieces"], "pieces")
	if seq > ss.committed {
		p.BadRequestf("seq is above the highest seq handed out")
	}
	if bytes > MaxAttachmentBytes {
		p.Fail(p.TooLarge, "an attachment has at most 16 MB", "maxBytes", MaxAttachmentBytes)
	}
	if wire > PartBytes {
		p.Fail(p.TooLarge, "part over 1 MB", "maxBytes", PartBytes)
	}
	a := ss.attachments[checksum]
	if a != nil && a.Complete {
		if seq > a.Seq {
			a.Seq = seq
			s.dirty(ss)
		}
		return p.O("ok", true, "complete", true)
	}
	if a != nil && (a.Parts != parts || a.Bytes != bytes) {
		p.BadRequestf("parts or bytes differ from the upload of this checksum that has begun")
	}
	if a == nil {
		a = &attachment{Checksum: checksum, Bytes: bytes, Parts: parts, Seq: seq, By: pt.ID, Received: map[int]bool{}}
		ss.attachments[checksum] = a
	}
	if !a.Received[part] {
		var n int64
		for _, pc := range pieces {
			n += int64(len(pc))
		}
		if a.got+n > MaxAttachmentBytes {
			p.Fail(p.TooLarge, "an attachment has at most 16 MB", "maxBytes", MaxAttachmentBytes)
		}
		must(0, s.deps.Attachments.PutPart(ss.ID, checksum, part, pieces))
		a.Received[part] = true
		a.got += n
		a.Seq = max(a.Seq, seq)
		s.dirty(ss)
	}
	if len(a.Received) == a.Parts {
		a.Complete = true
		a.Received = nil
		a.At = s.now()
		a.By = pt.ID
		ss.attachmentUploaders[checksum] = pt.ID
		s.saveRecord(ss)
	}
	return p.O("ok", true, "complete", a.Complete)
}

func (s *Service) hAttachmentGet(r *p.R, _ RequestContext, u *User) *p.Obj {
	s.mu.Lock()
	defer s.mu.Unlock()
	ss, _ := s.member(r, *u)
	checksum := r.String("checksum", 200, 1)
	part := int(r.OptInt("part", 0, 0, p.MaxInt))
	a := ss.attachments[checksum]
	if a == nil || !a.Complete {
		p.Fail(p.AttachmentUnknown, "")
	}
	if part >= a.Parts {
		p.BadRequestf("part out of range")
	}
	pieces, ok := must2(s.deps.Attachments.GetPart(ss.ID, checksum, part))
	if !ok {
		p.Fail(p.AttachmentUnknown, "")
	}
	if pieces == nil {
		pieces = []string{}
	}
	return p.O("ok", true, "checksum", checksum, "bytes", a.Bytes, "part", part, "parts", a.Parts, "pieces", pieces)
}

func (s *Service) hAttachmentWant(r *p.R, _ RequestContext, u *User) *p.Obj {
	s.mu.Lock()
	defer s.mu.Unlock()
	ss, pt := s.member(r, *u)
	checksum := r.String("checksum", 200, 1)
	if a := ss.attachments[checksum]; a != nil && a.Complete {
		return p.O("ok", true)
	}
	var to []*participant
	if up := ss.participants[ss.attachmentUploaders[checksum]]; up != nil && up.Status == "active" && up != pt {
		to = []*participant{up}
	} else {
		for _, other := range s.active(ss) {
			if other != pt {
				to = append(to, other)
			}
		}
	}
	now := s.now()
	woke := false
	for _, other := range to {
		key := other.ID + "\x00" + checksum
		if last, ok := ss.wanted[key]; ok && now-last < wantGapMs {
			continue
		}
		ss.wanted[key] = now
		s.pushNotice(other, notice{Kind: "attachment_wanted", Checksum: checksum})
		woke = true
	}
	if woke {
		s.notify(ss)
	}
	return p.O("ok", true)
}

// dropPartialAttachments forgets the incomplete uploads a participant began; s.mu is held.
func (s *Service) dropPartialAttachments(ss *session, pt *participant) {
	for sum, a := range ss.attachments {
		if !a.Complete && a.By == pt.ID {
			delete(ss.attachments, sum)
			if err := s.deps.Attachments.Drop(ss.ID, sum); err != nil {
				s.logError("could not drop an attachment", err)
			}
		}
	}
}

func (s *Service) dropAttachments(ss *session) {
	ss.attachments = map[string]*attachment{}
	if err := s.deps.Attachments.DropSession(ss.ID); err != nil {
		s.logError("could not drop the attachments of "+ss.ID, err)
	}
}

// ForgetAttachment drops one complete attachment as if the server had lost it (for test controls).
func (s *Service) ForgetAttachment(sessionID, checksum string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	ss := s.sessions[sessionID]
	if ss == nil || ss.attachments[checksum] == nil {
		return false
	}
	delete(ss.attachments, checksum)
	_ = s.deps.Attachments.Drop(ss.ID, checksum)
	s.saveRecord(ss)
	return true
}

func must2[A, B any](a A, b B, err error) (A, B) {
	if err != nil {
		must(0, err)
	}
	return a, b
}
