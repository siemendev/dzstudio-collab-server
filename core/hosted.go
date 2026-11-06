package core

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"

	p "github.com/siemendev/dzstudio-collab-server/protocol"
)

func (s *Service) hostedLock(ref HostedRef) *sync.Mutex {
	key := ref.Provider + "\x00" + ref.Key
	l := s.hostedLocks[key]
	if l == nil {
		l = &sync.Mutex{}
		s.hostedLocks[key] = l
	}
	return l
}

func sha256Hex(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// hProjectSave follows the order of project/save: rights and existence, then (1) fields, (2) the checksum of every
// file over the bytes that arrived (also with force), (3) not_confirmed, (4) superseded, (5) without force
// store_changed, (6) without force the merged states (the store's part).
func (s *Service) hProjectSave(r *p.R, _ RequestContext, u *User) *p.Obj {
	var ss *session
	var pt *participant
	var store ProjectStore
	func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		ss, pt = s.member(r, *u)
		if ss.Kind != "hosted" {
			p.BadRequestf("project/save is only for hosted sessions")
		}
		if !pt.CanSave {
			p.Fail(p.NotAllowed, "you may not save this project")
		}
		store = s.store(ss.HostedRef.Provider)
		if store == nil {
			p.Fail(p.NotAllowed, "this server has no project store for this project")
		}
	}()

	seq := r.Int("seq", 0, p.MaxInt)
	items := r.Array("files", 100_000)
	files := make([]ProjectFile, len(items))
	names := map[string]bool{}
	for i, it := range items {
		fr := p.Item(it, "files")
		name := fr.String("name", 200, 1)
		pieces, _ := p.Pieces(fr.M["pieces"], "files.pieces")
		if !fr.Has("checksum") {
			p.BadRequestf("file %s has no checksum", name)
		}
		checksum := fr.String("checksum", 200, 1)
		if names[name] {
			p.BadRequestf("file %s appears twice", name)
		}
		names[name] = true
		files[i] = ProjectFile{Name: name, Content: strings.Join(pieces, ""), Checksum: checksum}
	}
	var deleted []string
	if list, ok := r.OptArray("deleted", 100_000); ok {
		for _, d := range list {
			name, ok := d.(string)
			if !ok || name == "" || len(name) > 200 {
				p.BadRequestf("deleted must be file names")
			}
			if names[name] {
				p.BadRequestf("file %s is both written and deleted", name)
			}
			deleted = append(deleted, name)
		}
	}
	var merged []MergedChecksum
	if list, ok := r.OptArray("merged", 100_000); ok {
		merged = []MergedChecksum{}
		targets := map[string]bool{}
		for _, it := range list {
			mr := p.Item(it, "merged")
			target := mr.String("target", 200, 1)
			if targets[target] {
				p.BadRequestf("merged names %s twice", target)
			}
			targets[target] = true
			merged = append(merged, MergedChecksum{Target: target, Checksum: mr.String("checksum", 200, 1)})
		}
	}
	force := r.Bool("force")
	requestID, _ := r.OptString("requestId", 64)
	func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if seq <= ss.committed && s.insideChain(ss, seq) {
			p.BadRequestf("seq lies inside a chain")
		}
	}()

	var mismatches []*p.Obj
	for _, f := range files {
		if actual := sha256Hex(f.Content); actual != f.Checksum {
			mismatches = append(mismatches, p.O("kind", "file", "name", f.Name, "expected", f.Checksum, "actual", actual))
		}
	}
	if len(mismatches) > 0 {
		p.Fail(p.ChecksumMismatch, "a file did not arrive as sent; nothing was written", "mismatches", mismatches)
	}

	if func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return seq > ss.committed
	}() {
		p.Fail(p.NotConfirmed, "")
	}

	ref := *ss.HostedRef
	s.mu.Lock()
	lk := s.hostedLock(ref)
	s.mu.Unlock()
	lk.Lock()
	defer lk.Unlock()

	var base, created string
	var superseded *p.Obj
	func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if ss.ended != nil {
			failEnded(ss.ended)
		}
		h := ss.hosted
		if seq <= h.SavedSeq && h.BaseRevision != "" {
			superseded = p.O("ok", true, "savedSeq", h.SavedSeq, "hostedRevision", h.BaseRevision, "superseded", true)
		}
		base, created = h.BaseRevision, h.CreatedRevision
	}()
	if superseded != nil {
		return superseded
	}
	if !force {
		changed := must(store.ChangedSince(ref.Key, base))
		if len(changed) > 0 {
			failStoreChanged(changed)
		}
	}
	s.mu.Lock()
	ss.hosted.writing = true
	ss.ver.Saving++
	s.notify(ss)
	s.mu.Unlock()
	res, err := store.Save(SaveRequest{
		Key: ref.Key, Files: files, Deleted: deleted, Merged: merged, Force: force,
		BaseRevision: base, CreatedRevision: created, SessionID: ss.ID, RequestID: requestID, User: pt.User,
		Meta: ProjectMeta{ProjectName: ss.ProjectName, Map: ss.Map},
	})

	s.mu.Lock()
	defer s.mu.Unlock()
	h := ss.hosted
	h.writing = false
	ss.ver.Saving++
	s.notify(ss)
	if err != nil {
		var mm *ChecksumMismatchError
		if errors.As(err, &mm) {
			list := []*p.Obj{}
			for _, m := range mm.Mismatches {
				o := p.O("kind", m.Kind, "name", p.TruncateUTF8(m.Name, 200))
				if m.Expected != "" {
					o.Set("expected", p.TruncateUTF8(m.Expected, 200))
				}
				if m.Actual != "" {
					o.Set("actual", p.TruncateUTF8(m.Actual, 200))
				}
				list = append(list, o)
			}
			p.Fail(p.ChecksumMismatch, "the store found a checksum that differs; nothing was written", "mismatches", list)
		}
		var pe *p.Error
		if errors.As(err, &pe) {
			panic(pe)
		}
		s.logError("save of "+ref.Provider+"/"+ref.Key+" failed", err)
		p.Fail(p.Internal, "saving to the project store failed")
	}
	ns := s.nsAt(ss, seq)
	if seq > h.SavedSeq || h.BaseRevision == "" {
		now := s.now()
		saver := pt.User
		h.SavedSeq, h.SavedNS, h.SavedBy, h.SavedAt, h.BaseRevision = seq, ns, &saver, &now, res.Revision
	}
	s.dirty(ss)
	checksums := map[string]string{}
	revs := []*p.Obj{}
	for _, f := range res.Files {
		sum := f.Checksum
		if sum == "" {
			for _, sent := range files {
				if sent.Name == f.Name {
					sum = sha256Hex(sent.Content)
				}
			}
		}
		checksums[f.Name] = sum
		revs = append(revs, p.O("name", f.Name, "revision", p.TruncateUTF8(f.Revision, 200), "checksum", sum))
	}
	mergedOut := []*p.Obj{}
	if res.Merged == nil {
		for _, f := range res.Files {
			mergedOut = append(mergedOut, p.O("target", f.Name, "checksum", checksums[f.Name]))
		}
	} else {
		for _, m := range res.Merged {
			mergedOut = append(mergedOut, p.O("target", p.TruncateUTF8(m.Target, 200), "checksum", p.TruncateUTF8(m.Checksum, 200)))
		}
	}
	return p.O("ok", true, "savedSeq", seq, "hostedRevision", res.Revision, "superseded", false, "revisions", revs, "merged", mergedOut)
}

func failStoreChanged(changed []string) {
	if len(changed) > 50 {
		changed = changed[:50]
	}
	p.Fail(p.StoreChanged, "the project was changed outside the session", "changed", changed)
}

// ------------------------------------------------------------ projects at the server

var pageTokenRE = regexp.MustCompile(`^o([0-9]{1,9})$`)

type listed struct {
	ref  HostedRef
	info ProjectInfo
}

func (s *Service) projectContext(ref HostedRef, name, mapKey string) ProjectContext {
	return ProjectContext{Kind: "hosted", HostedRef: ref, ProjectName: name, Map: mapKey}
}

// sessionFor names the running session of a project the user may enter (as sessions/list with hostedRef).
func (s *Service) sessionFor(ref HostedRef, u User) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	ss := s.runningHosted(ref)
	if ss == nil || ss.snapshot == nil || s.accessOf(ss, u) == "" {
		return ""
	}
	return ss.ID
}

func (s *Service) hProjectList(r *p.R, _ RequestContext, u *User) *p.Obj {
	offset := 0
	if tok, ok := r.OptString("after", 200); ok {
		m := pageTokenRE.FindStringSubmatch(tok)
		if m == nil {
			p.BadRequestf("after is not a mark this server gave")
		}
		offset, _ = strconv.Atoi(m[1])
	}
	type visibleProject struct {
		l    listed
		save bool
	}
	var mine []visibleProject
	for _, st := range s.deps.ProjectStores {
		if ul, ok := st.(UserProjectLister); ok {
			for _, lp := range must(ul.ListFor(*u)) {
				if lp.Rights.Join {
					mine = append(mine, visibleProject{listed{HostedRef{st.Provider(), lp.Info.Key}, lp.Info}, lp.Rights.Save})
				}
			}
			continue
		}
		for _, info := range must(st.List()) {
			ref := HostedRef{st.Provider(), info.Key}
			rights := must(s.deps.Permissions.Rights(*u, s.projectContext(ref, info.ProjectName, info.Map)))
			if rights.Join {
				mine = append(mine, visibleProject{listed{ref, info}, rights.Save})
			}
		}
	}
	sort.Slice(mine, func(i, j int) bool {
		a, b := mine[i].l.ref, mine[j].l.ref
		if a.Provider != b.Provider {
			return a.Provider < b.Provider
		}
		return a.Key < b.Key
	})
	stores := []*p.Obj{}
	creator, _ := s.deps.Permissions.(ProjectCreator)
	for _, st := range s.deps.ProjectStores {
		canCreate := false
		if creator != nil {
			canCreate = must(creator.MayCreate(*u, st.Provider()))
		}
		stores = append(stores, p.O("provider", st.Provider(), "canCreate", canCreate))
	}
	out := []*p.Obj{}
	end := min(offset+listPageSize, len(mine))
	for i := min(offset, len(mine)); i < end; i++ {
		v := mine[i]
		o := p.O("hostedRef", refObj(v.l.ref), "projectName", p.TruncateUTF8(v.l.info.ProjectName, 200), "map", p.TruncateUTF8(v.l.info.Map, 64),
			"hostedRevision", v.l.info.Revision, "canSave", v.save)
		if sid := s.sessionFor(v.l.ref, *u); sid != "" {
			o.Set("sessionId", sid)
		}
		out = append(out, o)
	}
	res := p.O("ok", true, "projects", out, "stores", stores)
	if end < len(mine) {
		res.Set("next", "o"+strconv.Itoa(end))
	}
	return res
}

// head and readFile go through the store, with the user for a store that checks rights itself.
func head(st ProjectStore, u User, key, revision string) (*ProjectHead, error) {
	if us, ok := st.(UserScopedStore); ok {
		return us.HeadFor(u, key, revision)
	}
	return st.Head(key, revision)
}

func readFile(st ProjectStore, u User, key, revision, name string) (string, error) {
	if us, ok := st.(UserScopedStore); ok {
		return us.ReadFileFor(u, key, revision, name)
	}
	return st.ReadFile(key, revision, name)
}

// openProject finds the head of a project the user may load. Rights come before existence and before the store is
// asked anything: who would have no right under this hostedRef gets not_allowed, whether or not the store (or any
// store) has something there.
func (s *Service) openProject(r *p.R, u User, revision string) (HostedRef, ProjectStore, *ProjectHead, ProjectRights) {
	ref := readHostedRef(r)
	rights := must(s.deps.Permissions.Rights(u, s.projectContext(ref, ref.Key, "")))
	if !rights.Join {
		p.Fail(p.NotAllowed, "you have no right to this project")
	}
	st := s.store(ref.Provider)
	if st == nil {
		p.Fail(p.ProjectUnknown, "")
	}
	h, err := head(st, u, ref.Key, revision)
	switch {
	case errors.Is(err, ErrProjectUnknown):
		p.Fail(p.ProjectUnknown, "")
	case errors.Is(err, ErrRevisionUnknown):
		p.Fail(p.RevisionUnknown, "")
	case err != nil:
		must(0, err)
	}
	return ref, st, h, rights
}

func (s *Service) projectHeadFields(o *p.Obj, ref HostedRef, head *ProjectHead, rights ProjectRights, u User) {
	o.Set("projectName", p.TruncateUTF8(head.ProjectName, 200))
	o.Set("map", p.TruncateUTF8(head.Map, 64))
	o.Set("canSave", rights.Save)
	if sid := s.sessionFor(ref, u); sid != "" {
		o.Set("sessionId", sid)
	}
}

func (s *Service) hProjectRevision(r *p.R, _ RequestContext, u *User) *p.Obj {
	ref, _, head, rights := s.openProject(r, *u, "")
	o := p.O("ok", true, "hostedRevision", head.Revision)
	s.projectHeadFields(o, ref, head, rights, *u)
	return o
}

func (s *Service) fileLayout(st ProjectStore, u User, key, revision string, f FileInfo) layout {
	if f.Hash != "" {
		s.mu.Lock()
		l, ok := s.layouts[f.Hash]
		s.mu.Unlock()
		if ok {
			return l
		}
	}
	content, err := readFile(st, u, key, revision, f.Name)
	if errors.Is(err, ErrRevisionUnknown) {
		p.Fail(p.RevisionUnknown, "")
	}
	must(0, err)
	l := layout{bytes: int64(len(content)), parts: len(p.GroupParts(p.SplitPieces(content), PartBytes))}
	if f.Hash != "" {
		s.mu.Lock()
		if len(s.layouts) > 100_000 {
			s.layouts = map[string]layout{}
		}
		s.layouts[f.Hash] = l
		s.mu.Unlock()
	}
	return l
}

func (s *Service) hProjectLoad(r *p.R, _ RequestContext, u *User) *p.Obj {
	revision, hasRevision := r.OptString("hostedRevision", 200)
	file, hasFile := r.OptString("file", 200)
	if hasFile && !hasRevision {
		p.BadRequestf("hostedRevision is needed to load a file")
	}
	if !hasFile && r.Has("part") {
		p.BadRequestf("part only comes with file")
	}
	if !hasFile {
		ref, st, head, rights := s.openProject(r, *u, revision)
		if len(head.Files) > maxHeadFiles {
			p.Fail(p.TooLarge, "the project has more than 5000 files", "maxCount", maxHeadFiles)
		}
		files := []*p.Obj{}
		for _, f := range head.Files {
			if len(f.Name) > 200 {
				continue
			}
			l := s.fileLayout(st, *u, ref.Key, head.Revision, f)
			o := p.O("name", f.Name, "bytes", l.bytes, "parts", l.parts)
			if f.Revision != "" {
				o.Set("revision", p.TruncateUTF8(f.Revision, 200))
			}
			files = append(files, o)
		}
		o := p.O("ok", true, "hostedRevision", head.Revision)
		s.projectHeadFields(o, ref, head, rights, *u)
		o.Set("files", files)
		return o
	}
	part := int(r.OptInt("part", 0, 0, p.MaxInt))
	ref, st, head, _ := s.openProject(r, *u, revision)
	content, err := readFile(st, *u, ref.Key, head.Revision, file)
	switch {
	case errors.Is(err, ErrRevisionUnknown):
		p.Fail(p.RevisionUnknown, "")
	case errors.Is(err, ErrFileUnknown):
		p.BadRequestf("this revision has no file %s", file)
	case errors.Is(err, ErrProjectUnknown):
		p.Fail(p.ProjectUnknown, "")
	case err != nil:
		must(0, err)
	}
	parts := p.GroupParts(p.SplitPieces(content), PartBytes)
	if part >= len(parts) {
		p.BadRequestf("part out of range")
	}
	return p.O("ok", true, "hostedRevision", head.Revision, "file", file, "part", part, "parts", len(parts), "pieces", parts[part])
}
