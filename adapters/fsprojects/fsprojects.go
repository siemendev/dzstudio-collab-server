// Package fsprojects keeps hosted projects as directories on disk.
//
// <root>/<key> is a symbolic link to the current version directory <root>/.versions/<key>/r<N>, described by
// <root>/.versions/<key>/r<N>.json (per file its SHA-256, size and revision). A save builds version N+1 completely
// and switches the link with one rename, so readers see either the old or the new project, never a mix. A plain
// directory placed at <root>/<key> becomes version r0. Files edited in place (through the link) are changes from
// outside: the next look at the project takes them over as a new version. A replaced version stays readable for at
// least 10 minutes. Name and map of a project live in <root>/.meta/<key>.json ({"projectName","map"}); without it
// the name is the key and the map the store's default.
package fsprojects

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/siemendev/dzstudio-collab-server/core"
	p "github.com/siemendev/dzstudio-collab-server/protocol"
)

var keyRE = regexp.MustCompile(`^[A-Za-z0-9_-][A-Za-z0-9._-]{0,199}$`)
var revRE = regexp.MustCompile(`^r([0-9]{1,9})$`)

// KeepReplacedMs is how long a version replaced by a save stays readable.
const KeepReplacedMs = 10 * 60_000

type fileState struct {
	SHA   string `json:"sha"`
	Rev   int    `json:"rev"`
	Bytes int64  `json:"bytes"`
}

type versionState struct {
	Revision  int                  `json:"revision"`
	CreatedAt int64                `json:"createdAt"`
	Files     map[string]fileState `json:"files"`
}

type meta struct {
	ProjectName string `json:"projectName"`
	Map         string `json:"map"`
}

type cached struct {
	signature string
	revision  int
}

type Store struct {
	provider   string
	root       string
	defaultMap string
	now        func() int64

	mu    sync.Mutex
	locks map[string]*sync.Mutex
	cache map[string]cached
}

func New(provider, root, defaultMap string, now func() int64) *Store {
	return &Store{provider: provider, root: root, defaultMap: defaultMap, now: now, locks: map[string]*sync.Mutex{}, cache: map[string]cached{}}
}

func (s *Store) Provider() string { return s.provider }

func validKey(key string) bool { return keyRE.MatchString(key) && !strings.Contains(key, "..") }

func (s *Store) lock(key string) func() {
	s.mu.Lock()
	l := s.locks[key]
	if l == nil {
		l = &sync.Mutex{}
		s.locks[key] = l
	}
	s.mu.Unlock()
	l.Lock()
	return l.Unlock
}

func (s *Store) link(key string) string     { return filepath.Join(s.root, key) }
func (s *Store) versions(key string) string { return filepath.Join(s.root, ".versions", key) }
func (s *Store) versionDir(key string, n int) string {
	return filepath.Join(s.versions(key), "r"+strconv.Itoa(n))
}

func encodeName(name string) string {
	var b strings.Builder
	for i := 0; i < len(name); i++ {
		c := name[i]
		ok := c >= 0x80 || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-' || (c == '.' && i > 0)
		if ok {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

func decodeName(file string) string {
	if n, err := url.PathUnescape(file); err == nil {
		return n
	}
	return file
}

func sum(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

func (s *Store) state(key string, n int) (*versionState, error) {
	data, err := os.ReadFile(filepath.Join(s.versions(key), "r"+strconv.Itoa(n)+".json"))
	if err != nil {
		return nil, err
	}
	var st versionState
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, err
	}
	return &st, nil
}

func (s *Store) writeState(key string, st *versionState) error {
	data, err := json.Marshal(st)
	if err != nil {
		return err
	}
	path := filepath.Join(s.versions(key), "r"+strconv.Itoa(st.Revision)+".json")
	if err := os.WriteFile(path+".tmp", data, 0o644); err != nil {
		return err
	}
	return os.Rename(path+".tmp", path)
}

type liveFile struct {
	sha   string
	bytes int64
}

func (s *Store) hashDir(dir string) (map[string]liveFile, error) {
	out := map[string]liveFile{}
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if e.IsDir() || strings.HasSuffix(e.Name(), ".tmp") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		out[decodeName(e.Name())] = liveFile{sum(data), int64(len(data))}
	}
	return out, nil
}

func (s *Store) signature(dir string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	var b strings.Builder
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			return ""
		}
		fmt.Fprintf(&b, "%s|%d|%d;", e.Name(), info.Size(), info.ModTime().UnixNano())
	}
	return b.String()
}

// current gives the current version number and takes over a plain directory or edits made in place; -1 if there is
// no such project. The key's lock is held.
func (s *Store) current(key string) (int, error) {
	link := s.link(key)
	fi, err := os.Lstat(link)
	if errors.Is(err, fs.ErrNotExist) {
		return -1, nil
	}
	if err != nil {
		return 0, err
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		if !fi.IsDir() {
			return 0, fmt.Errorf("%s is neither a directory nor a link", link)
		}
		if err := os.MkdirAll(s.versions(key), 0o755); err != nil {
			return 0, err
		}
		files, err := s.hashDir(link)
		if err != nil {
			return 0, err
		}
		st := &versionState{Revision: 0, CreatedAt: s.now(), Files: map[string]fileState{}}
		for name, f := range files {
			st.Files[name] = fileState{SHA: f.sha, Rev: 1, Bytes: f.bytes}
		}
		if err := s.writeState(key, st); err != nil {
			return 0, err
		}
		if err := os.Rename(link, s.versionDir(key, 0)); err != nil {
			return 0, err
		}
		if err := os.Symlink(filepath.Join(".versions", key, "r0"), link); err != nil {
			return 0, err
		}
		return 0, nil
	}
	target, err := os.Readlink(link)
	if err != nil {
		return 0, err
	}
	m := regexp.MustCompile(`r([0-9]+)$`).FindStringSubmatch(target)
	if m == nil {
		return 0, fmt.Errorf("unexpected link target %s", target)
	}
	n, _ := strconv.Atoi(m[1])
	sig := s.signature(link)
	s.mu.Lock()
	c, ok := s.cache[key]
	s.mu.Unlock()
	if ok && c.revision == n && c.signature == sig && sig != "" {
		return n, nil
	}
	st, err := s.state(key, n)
	if err != nil {
		return 0, err
	}
	live, err := s.hashDir(link)
	if err != nil {
		return 0, err
	}
	if !sameFiles(st.Files, live) {
		next, err := s.newVersion(key, n, st, nil, nil)
		if err != nil {
			return 0, err
		}
		n = next
		sig = s.signature(link)
	}
	s.mu.Lock()
	s.cache[key] = cached{sig, n}
	s.mu.Unlock()
	return n, nil
}

func sameFiles(recorded map[string]fileState, live map[string]liveFile) bool {
	if len(recorded) != len(live) {
		return false
	}
	for name, f := range live {
		if recorded[name].SHA != f.sha {
			return false
		}
	}
	return true
}

// newVersion builds version n+1 from the current files plus writes and deletes, then switches the link.
func (s *Store) newVersion(key string, n int, prev *versionState, files []core.ProjectFile, deleted []string) (int, error) {
	next := max(n+1, 1)
	dir := s.versionDir(key, next)
	if err := os.RemoveAll(dir); err != nil {
		return 0, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return 0, err
	}
	if n >= 0 {
		entries, err := os.ReadDir(s.link(key))
		if err != nil {
			return 0, err
		}
		for _, e := range entries {
			if e.IsDir() || strings.HasSuffix(e.Name(), ".tmp") {
				continue
			}
			data, err := os.ReadFile(filepath.Join(s.link(key), e.Name()))
			if err != nil {
				return 0, err
			}
			if err := os.WriteFile(filepath.Join(dir, e.Name()), data, 0o644); err != nil {
				return 0, err
			}
		}
	}
	for _, name := range deleted {
		if err := os.Remove(filepath.Join(dir, encodeName(name))); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return 0, err
		}
	}
	for _, f := range files {
		if err := os.WriteFile(filepath.Join(dir, encodeName(f.Name)), []byte(f.Content), 0o644); err != nil {
			return 0, err
		}
	}
	hashes, err := s.hashDir(dir)
	if err != nil {
		return 0, err
	}
	var mismatches []core.Mismatch
	for _, f := range files {
		if got := hashes[f.Name].sha; f.Checksum != "" && got != f.Checksum {
			mismatches = append(mismatches, core.Mismatch{Kind: "file", Name: f.Name, Expected: f.Checksum, Actual: got})
		}
	}
	if len(mismatches) > 0 {
		_ = os.RemoveAll(dir)
		return 0, &core.ChecksumMismatchError{Mismatches: mismatches}
	}
	st := &versionState{Revision: next, CreatedAt: s.now(), Files: map[string]fileState{}}
	for name, f := range hashes {
		rev := 1
		if prev != nil {
			if before, ok := prev.Files[name]; ok {
				rev = before.Rev
				if before.SHA != f.sha {
					rev++
				}
			}
		}
		st.Files[name] = fileState{SHA: f.sha, Rev: rev, Bytes: f.bytes}
	}
	if err := s.writeState(key, st); err != nil {
		return 0, err
	}
	tmp := s.link(key) + ".link.tmp"
	_ = os.Remove(tmp)
	if err := os.Symlink(filepath.Join(".versions", key, "r"+strconv.Itoa(next)), tmp); err != nil {
		return 0, err
	}
	if err := os.Rename(tmp, s.link(key)); err != nil {
		return 0, err
	}
	s.cleanup(key, next)
	return next, nil
}

// cleanup removes version directories replaced more than 10 minutes ago (their descriptions stay).
func (s *Store) cleanup(key string, current int) {
	now := s.now()
	for n := current - 1; n >= 0; n-- {
		dir := s.versionDir(key, n)
		if _, err := os.Stat(dir); err != nil {
			continue
		}
		replacedBy, err := s.state(key, n+1)
		if err != nil || now-replacedBy.CreatedAt < KeepReplacedMs {
			continue
		}
		_ = os.RemoveAll(dir)
	}
}

func (s *Store) CurrentRevision(key string) (string, bool, error) {
	if !validKey(key) {
		return "", false, nil
	}
	defer s.lock(key)()
	n, err := s.current(key)
	if err != nil || n < 0 {
		return "", false, err
	}
	return "r" + strconv.Itoa(n), true, nil
}

func (s *Store) ChangedSince(key, revision string) ([]string, error) {
	if !validKey(key) {
		return nil, nil
	}
	defer s.lock(key)()
	n, err := s.current(key)
	if err != nil {
		return nil, err
	}
	if n < 0 {
		if revision == "" {
			return nil, nil
		}
		return []string{"(project removed)"}, nil
	}
	if revision == "r"+strconv.Itoa(n) {
		return nil, nil
	}
	cur, err := s.state(key, n)
	if err != nil {
		return nil, err
	}
	var base *versionState
	if m := revRE.FindStringSubmatch(revision); m != nil {
		b, _ := strconv.Atoi(m[1])
		base, _ = s.state(key, b)
	}
	changed := map[string]bool{}
	if base == nil {
		for name := range cur.Files {
			changed[name] = true
		}
	} else {
		for name, f := range cur.Files {
			if base.Files[name].SHA != f.SHA {
				changed[name] = true
			}
		}
		for name := range base.Files {
			if _, ok := cur.Files[name]; !ok {
				changed[name] = true
			}
		}
	}
	out := make([]string, 0, len(changed))
	for name := range changed {
		out = append(out, name)
	}
	sort.Strings(out)
	return out, nil
}

// Save writes a complete new version. It composes nothing, so merged expectations are not looked at; the checksum
// of every file is formed over the bytes written into the new version and checked before the link switches.
func (s *Store) Save(req core.SaveRequest) (core.SaveResult, error) {
	key, files, deleted, m := req.Key, req.Files, req.Deleted, req.Meta
	if !validKey(key) {
		return core.SaveResult{}, p.NewError(p.NotAllowed, "this project key is not valid for this store")
	}
	defer s.lock(key)()
	if err := os.MkdirAll(s.root, 0o755); err != nil {
		return core.SaveResult{}, err
	}
	n, err := s.current(key)
	if err != nil {
		return core.SaveResult{}, err
	}
	var prev *versionState
	if n >= 0 {
		if prev, err = s.state(key, n); err != nil {
			return core.SaveResult{}, err
		}
	} else if err := os.MkdirAll(s.versions(key), 0o755); err != nil {
		return core.SaveResult{}, err
	}
	next, err := s.newVersion(key, n, prev, files, deleted)
	if err != nil {
		return core.SaveResult{}, err
	}
	if _, err := os.Stat(s.metaPath(key)); errors.Is(err, fs.ErrNotExist) && (m.ProjectName != "" || m.Map != "") {
		_ = s.writeMeta(key, meta{ProjectName: m.ProjectName, Map: m.Map})
	}
	st, err := s.state(key, next)
	if err != nil {
		return core.SaveResult{}, err
	}
	res := core.SaveResult{Revision: "r" + strconv.Itoa(next)}
	for _, f := range files {
		fs := st.Files[f.Name]
		res.Files = append(res.Files, core.FileRevision{Name: f.Name, Revision: strconv.Itoa(max(1, fs.Rev)), Checksum: fs.SHA})
	}
	return res, nil
}

func (s *Store) metaPath(key string) string { return filepath.Join(s.root, ".meta", key+".json") }

func (s *Store) readMeta(key string) meta {
	m := meta{ProjectName: key, Map: s.defaultMap}
	data, err := os.ReadFile(s.metaPath(key))
	if err != nil {
		return m
	}
	var stored meta
	if json.Unmarshal(data, &stored) == nil {
		if stored.ProjectName != "" {
			m.ProjectName = stored.ProjectName
		}
		if stored.Map != "" {
			m.Map = stored.Map
		}
	}
	return m
}

func (s *Store) writeMeta(key string, m meta) error {
	if err := os.MkdirAll(filepath.Join(s.root, ".meta"), 0o755); err != nil {
		return err
	}
	data, _ := json.Marshal(m)
	return os.WriteFile(s.metaPath(key), data, 0o644)
}

func (s *Store) List() ([]core.ProjectInfo, error) {
	entries, err := os.ReadDir(s.root)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []core.ProjectInfo
	for _, e := range entries {
		key := e.Name()
		if strings.HasPrefix(key, ".") || strings.HasSuffix(key, ".tmp") || !validKey(key) {
			continue
		}
		if e.Type()&os.ModeSymlink == 0 && !e.IsDir() {
			continue
		}
		rev, ok, err := s.CurrentRevision(key)
		if err != nil || !ok {
			continue
		}
		m := s.readMeta(key)
		out = append(out, core.ProjectInfo{Key: key, ProjectName: m.ProjectName, Map: m.Map, Revision: rev})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

// version finds a revision ("" = current) with its description; the key's lock is held.
func (s *Store) version(key, revision string) (int, *versionState, error) {
	if !validKey(key) {
		return 0, nil, core.ErrProjectUnknown
	}
	cur, err := s.current(key)
	if err != nil {
		return 0, nil, err
	}
	if cur < 0 {
		return 0, nil, core.ErrProjectUnknown
	}
	n := cur
	if revision != "" {
		m := revRE.FindStringSubmatch(revision)
		if m == nil {
			return 0, nil, core.ErrRevisionUnknown
		}
		n, _ = strconv.Atoi(m[1])
		if n > cur {
			return 0, nil, core.ErrRevisionUnknown
		}
		if _, err := os.Stat(s.versionDir(key, n)); err != nil {
			return 0, nil, core.ErrRevisionUnknown
		}
	}
	st, err := s.state(key, n)
	if err != nil {
		return 0, nil, core.ErrRevisionUnknown
	}
	return n, st, nil
}

func (s *Store) Head(key, revision string) (*core.ProjectHead, error) {
	if !validKey(key) {
		return nil, core.ErrProjectUnknown
	}
	defer s.lock(key)()
	n, st, err := s.version(key, revision)
	if err != nil {
		return nil, err
	}
	m := s.readMeta(key)
	head := &core.ProjectHead{Revision: "r" + strconv.Itoa(n), ProjectName: m.ProjectName, Map: m.Map}
	names := make([]string, 0, len(st.Files))
	for name := range st.Files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		f := st.Files[name]
		head.Files = append(head.Files, core.FileInfo{Name: name, Revision: strconv.Itoa(f.Rev), Bytes: f.Bytes, Hash: f.SHA})
	}
	return head, nil
}

func (s *Store) ReadFile(key, revision, name string) (string, error) {
	if !validKey(key) {
		return "", core.ErrProjectUnknown
	}
	defer s.lock(key)()
	n, st, err := s.version(key, revision)
	if err != nil {
		return "", err
	}
	f, ok := st.Files[name]
	if !ok {
		return "", core.ErrFileUnknown
	}
	data, err := os.ReadFile(filepath.Join(s.versionDir(key, n), encodeName(name)))
	if errors.Is(err, fs.ErrNotExist) {
		data, err = os.ReadFile(filepath.Join(s.versionDir(key, n), name))
	}
	if err != nil || sum(data) != f.SHA {
		return "", core.ErrRevisionUnknown
	}
	return string(data), nil
}

// Load reads the whole current project (for operators, tools and tests).
func (s *Store) Load(key string) (*core.ProjectHead, map[string]string, error) {
	head, err := s.Head(key, "")
	if err != nil {
		return nil, nil, err
	}
	files := map[string]string{}
	for _, f := range head.Files {
		c, err := s.ReadFile(key, head.Revision, f.Name)
		if err != nil {
			return nil, nil, err
		}
		files[f.Name] = c
	}
	return head, files, nil
}

// EditOutside writes files in place, as an operator editing the project outside any session would, and sets the
// project's name and map if given. For tests and tools.
func (s *Store) EditOutside(key string, files map[string]string, projectName, mapKey string) error {
	if !validKey(key) {
		return fmt.Errorf("invalid key %q", key)
	}
	defer s.lock(key)()
	if _, err := os.Lstat(s.link(key)); errors.Is(err, fs.ErrNotExist) {
		if err := os.MkdirAll(s.link(key), 0o755); err != nil {
			return err
		}
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(s.link(key), encodeName(name)), []byte(content), 0o644); err != nil {
			return err
		}
	}
	if projectName != "" || mapKey != "" {
		m := s.readMeta(key)
		if projectName != "" {
			m.ProjectName = projectName
		}
		if mapKey != "" {
			m.Map = mapKey
		}
		return s.writeMeta(key, m)
	}
	return nil
}
