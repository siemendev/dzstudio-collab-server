// Package memstore keeps the session log in memory only; everything is gone after a restart.
package memstore

import (
	"sort"
	"sync"

	"github.com/siemendev/dzstudio-collab-server/core"
)

type slot struct {
	record    []byte
	entries   []core.LogEntry
	snapshots map[int64]map[int]core.SnapshotPart
	staged    map[string][]core.StagedItem
	values    map[string][]byte
}

type Store struct {
	mu       sync.Mutex
	slots    map[string]*slot
	requests []core.StoredRequest
}

func New() *Store { return &Store{slots: map[string]*slot{}} }

func (m *Store) slot(id string) *slot {
	s := m.slots[id]
	if s == nil {
		s = &slot{snapshots: map[int64]map[int]core.SnapshotPart{}, staged: map[string][]core.StagedItem{}, values: map[string][]byte{}}
		m.slots[id] = s
	}
	return s
}

// Set puts a whole session (used by the file store while loading).
func (m *Store) Set(id string, st core.StoredSession, snapshots map[int64]map[int]core.SnapshotPart) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.slots, id)
	s := m.slot(id)
	s.record = st.Record
	s.entries = st.Entries
	if snapshots != nil {
		s.snapshots = snapshots
	}
	if st.Staged != nil {
		s.staged = st.Staged
	}
	if st.Values != nil {
		s.values = st.Values
	}
}

func (m *Store) SetRequests(r []core.StoredRequest) {
	m.mu.Lock()
	m.requests = r
	m.mu.Unlock()
}

func (m *Store) LoadAll() ([]core.StoredSession, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	ids := make([]string, 0, len(m.slots))
	for id := range m.slots {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var out []core.StoredSession
	for _, id := range ids {
		s := m.slots[id]
		if s.record == nil {
			continue
		}
		staged := map[string][]core.StagedItem{}
		for k, v := range s.staged {
			staged[k] = append([]core.StagedItem(nil), v...)
		}
		values := map[string][]byte{}
		for k, v := range s.values {
			values[k] = v
		}
		out = append(out, core.StoredSession{Record: s.record, Entries: append([]core.LogEntry(nil), s.entries...), Staged: staged, Values: values})
	}
	return out, nil
}

func (m *Store) SaveRecord(id string, record []byte) error {
	m.mu.Lock()
	m.slot(id).record = record
	m.mu.Unlock()
	return nil
}

func (m *Store) Append(id string, entries []core.LogEntry) error {
	m.mu.Lock()
	s := m.slot(id)
	s.entries = append(s.entries, entries...)
	m.mu.Unlock()
	return nil
}

func (m *Store) Read(id string, after int64, limit int) ([]core.LogEntry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.slots[id]
	if s == nil {
		return nil, nil
	}
	i := sort.Search(len(s.entries), func(i int) bool { return s.entries[i].Seq > after })
	end := min(len(s.entries), i+limit)
	return append([]core.LogEntry(nil), s.entries[i:end]...), nil
}

func (m *Store) Truncate(id string, through int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.slots[id]
	if s == nil {
		return nil
	}
	i := sort.Search(len(s.entries), func(i int) bool { return s.entries[i].Seq > through })
	s.entries = append([]core.LogEntry(nil), s.entries[i:]...)
	return nil
}

// Entries gives the kept log of a session (for the file store).
func (m *Store) Entries(id string) []core.LogEntry {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s := m.slots[id]; s != nil {
		return append([]core.LogEntry(nil), s.entries...)
	}
	return nil
}

func (m *Store) PutSnapshotPart(id string, part core.SnapshotPart) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.slot(id)
	parts := s.snapshots[part.Seq]
	if parts == nil {
		parts = map[int]core.SnapshotPart{}
		s.snapshots[part.Seq] = parts
	}
	parts[part.Part] = part
	return nil
}

func (m *Store) GetSnapshotPart(id string, seq int64, part int) (*core.SnapshotPart, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.slots[id]
	if s == nil {
		return nil, nil
	}
	sp, ok := s.snapshots[seq][part]
	if !ok {
		return nil, nil
	}
	return &sp, nil
}

func (m *Store) DropSnapshot(id string, seq int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s := m.slots[id]; s != nil {
		delete(s.snapshots, seq)
	}
	return nil
}

func (m *Store) Stage(id, pid string, items []core.StagedItem) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.slot(id)
	s.staged[pid] = append(s.staged[pid], items...)
	return nil
}

func (m *Store) Unstage(id, pid string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s := m.slots[id]; s != nil {
		delete(s.staged, pid)
	}
	return nil
}

func (m *Store) PutValue(id, key string, value []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.slot(id).values[key] = value
	return nil
}

func (m *Store) DeleteSession(id string) error {
	m.mu.Lock()
	delete(m.slots, id)
	m.mu.Unlock()
	return nil
}

func (m *Store) AppendRequest(r core.StoredRequest) error {
	m.mu.Lock()
	m.requests = append(m.requests, r)
	m.mu.Unlock()
	return nil
}

func (m *Store) LoadRequests() ([]core.StoredRequest, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]core.StoredRequest(nil), m.requests...), nil
}

func (m *Store) CompactRequests(keep []core.StoredRequest) error {
	m.mu.Lock()
	m.requests = append([]core.StoredRequest(nil), keep...)
	m.mu.Unlock()
	return nil
}

// IDs names every session the store holds.
func (m *Store) IDs() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	ids := make([]string, 0, len(m.slots))
	for id := range m.slots {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// Export gives everything kept for one session, as Set takes it back (for a log store writing a snapshot of it).
func (m *Store) Export(id string) (core.StoredSession, map[int64]map[int]core.SnapshotPart, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.slots[id]
	if s == nil {
		return core.StoredSession{}, nil, false
	}
	st := core.StoredSession{Record: s.record, Entries: append([]core.LogEntry(nil), s.entries...), Staged: map[string][]core.StagedItem{}, Values: map[string][]byte{}}
	for k, v := range s.staged {
		st.Staged[k] = append([]core.StagedItem(nil), v...)
	}
	for k, v := range s.values {
		st.Values[k] = v
	}
	snaps := map[int64]map[int]core.SnapshotPart{}
	for seq, parts := range s.snapshots {
		snaps[seq] = map[int]core.SnapshotPart{}
		for i, part := range parts {
			snaps[seq][i] = part
		}
	}
	return st, snaps, true
}
