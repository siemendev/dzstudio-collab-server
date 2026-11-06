// Package filestore keeps the session log in memory and in append-only files, so that it survives restarts:
// one directory per session with record.json (replaced atomically), log.jsonl (one entry per line),
// snapshots/<seq>/<part>.json, staged/<participantId>.jsonl and values/<key>.json, plus requests.jsonl for the
// retry window. Every call returns once its data is written.
package filestore

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"

	"github.com/siemendev/dzstudio-collab-server/adapters/memstore"
	"github.com/siemendev/dzstudio-collab-server/core"
)

var safe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

type Store struct {
	*memstore.Store
	root  string
	fsync bool
}

func New(root string, fsync bool) *Store {
	return &Store{Store: memstore.New(), root: root, fsync: fsync}
}

func (f *Store) dir(id string) (string, error) {
	if !safe.MatchString(id) {
		return "", fmt.Errorf("unsafe session id %q", id)
	}
	return filepath.Join(f.root, id), nil
}

func (f *Store) writeAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	fh, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err = fh.Write(data); err == nil && f.fsync {
		err = fh.Sync()
	}
	if cerr := fh.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (f *Store) appendFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	fh, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err = fh.Write(data); err == nil && f.fsync {
		err = fh.Sync()
	}
	if cerr := fh.Close(); err == nil {
		err = cerr
	}
	return err
}

func jsonLines[T any](items []T) ([]byte, error) {
	var b bytes.Buffer
	for _, it := range items {
		data, err := json.Marshal(it)
		if err != nil {
			return nil, err
		}
		b.Write(data)
		b.WriteByte('\n')
	}
	return b.Bytes(), nil
}

// readLines reads one JSON value per line and stops at the first broken line (a write cut by a crash).
func readLines[T any](path string) ([]T, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []T
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 1<<20), 1<<30)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var v T
		if json.Unmarshal(line, &v) != nil {
			break
		}
		out = append(out, v)
	}
	return out, nil
}

func (f *Store) LoadAll() ([]core.StoredSession, error) {
	if err := os.MkdirAll(f.root, 0o755); err != nil {
		return nil, err
	}
	names, err := os.ReadDir(f.root)
	if err != nil {
		return nil, err
	}
	for _, n := range names {
		if !n.IsDir() || !safe.MatchString(n.Name()) {
			continue
		}
		dir := filepath.Join(f.root, n.Name())
		record, err := os.ReadFile(filepath.Join(dir, "record.json"))
		if err != nil {
			continue
		}
		entries, err := readLines[core.LogEntry](filepath.Join(dir, "log.jsonl"))
		if err != nil {
			return nil, err
		}
		st := core.StoredSession{Record: record, Entries: entries, Staged: map[string][]core.StagedItem{}, Values: map[string][]byte{}}
		if staged, err := os.ReadDir(filepath.Join(dir, "staged")); err == nil {
			for _, s := range staged {
				pid, ok := cutSuffix(s.Name(), ".jsonl")
				if !ok {
					continue
				}
				items, err := readLines[core.StagedItem](filepath.Join(dir, "staged", s.Name()))
				if err != nil {
					return nil, err
				}
				st.Staged[pid] = items
			}
		}
		if values, err := os.ReadDir(filepath.Join(dir, "values")); err == nil {
			for _, v := range values {
				key, ok := cutSuffix(v.Name(), ".json")
				if !ok {
					continue
				}
				if data, err := os.ReadFile(filepath.Join(dir, "values", v.Name())); err == nil {
					st.Values[key] = data
				}
			}
		}
		snaps := map[int64]map[int]core.SnapshotPart{}
		if seqs, err := os.ReadDir(filepath.Join(dir, "snapshots")); err == nil {
			for _, sd := range seqs {
				seq, err := strconv.ParseInt(sd.Name(), 10, 64)
				if err != nil {
					continue
				}
				parts := map[int]core.SnapshotPart{}
				files, _ := os.ReadDir(filepath.Join(dir, "snapshots", sd.Name()))
				for _, pf := range files {
					if _, ok := cutSuffix(pf.Name(), ".json"); !ok {
						continue
					}
					data, err := os.ReadFile(filepath.Join(dir, "snapshots", sd.Name(), pf.Name()))
					if err != nil {
						continue
					}
					var sp core.SnapshotPart
					if json.Unmarshal(data, &sp) == nil {
						parts[sp.Part] = sp
					}
				}
				snaps[seq] = parts
			}
		}
		f.Store.Set(n.Name(), st, snaps)
	}
	reqs, err := readLines[core.StoredRequest](filepath.Join(f.root, "requests.jsonl"))
	if err != nil {
		return nil, err
	}
	f.Store.SetRequests(reqs)
	return f.Store.LoadAll()
}

func cutSuffix(name, suffix string) (string, bool) {
	if len(name) <= len(suffix) || name[len(name)-len(suffix):] != suffix {
		return "", false
	}
	return name[:len(name)-len(suffix)], true
}

func (f *Store) SaveRecord(id string, record []byte) error {
	dir, err := f.dir(id)
	if err != nil {
		return err
	}
	if err := f.writeAtomic(filepath.Join(dir, "record.json"), record); err != nil {
		return err
	}
	return f.Store.SaveRecord(id, record)
}

func (f *Store) Append(id string, entries []core.LogEntry) error {
	dir, err := f.dir(id)
	if err != nil {
		return err
	}
	data, err := jsonLines(entries)
	if err != nil {
		return err
	}
	if err := f.appendFile(filepath.Join(dir, "log.jsonl"), data); err != nil {
		return err
	}
	return f.Store.Append(id, entries)
}

func (f *Store) Truncate(id string, through int64) error {
	dir, err := f.dir(id)
	if err != nil {
		return err
	}
	if err := f.Store.Truncate(id, through); err != nil {
		return err
	}
	data, err := jsonLines(f.Store.Entries(id))
	if err != nil {
		return err
	}
	return f.writeAtomic(filepath.Join(dir, "log.jsonl"), data)
}

func (f *Store) PutSnapshotPart(id string, part core.SnapshotPart) error {
	dir, err := f.dir(id)
	if err != nil {
		return err
	}
	data, err := json.Marshal(part)
	if err != nil {
		return err
	}
	if err := f.writeAtomic(filepath.Join(dir, "snapshots", strconv.FormatInt(part.Seq, 10), strconv.Itoa(part.Part)+".json"), data); err != nil {
		return err
	}
	return f.Store.PutSnapshotPart(id, part)
}

func (f *Store) DropSnapshot(id string, seq int64) error {
	dir, err := f.dir(id)
	if err != nil {
		return err
	}
	if err := os.RemoveAll(filepath.Join(dir, "snapshots", strconv.FormatInt(seq, 10))); err != nil {
		return err
	}
	return f.Store.DropSnapshot(id, seq)
}

func (f *Store) Stage(id, pid string, items []core.StagedItem) error {
	dir, err := f.dir(id)
	if err != nil || !safe.MatchString(pid) {
		return fmt.Errorf("unsafe ids %q/%q", id, pid)
	}
	data, err := jsonLines(items)
	if err != nil {
		return err
	}
	if err := f.appendFile(filepath.Join(dir, "staged", pid+".jsonl"), data); err != nil {
		return err
	}
	return f.Store.Stage(id, pid, items)
}

func (f *Store) Unstage(id, pid string) error {
	dir, err := f.dir(id)
	if err != nil || !safe.MatchString(pid) {
		return fmt.Errorf("unsafe ids %q/%q", id, pid)
	}
	if err := os.Remove(filepath.Join(dir, "staged", pid+".jsonl")); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return f.Store.Unstage(id, pid)
}

func (f *Store) PutValue(id, key string, value []byte) error {
	dir, err := f.dir(id)
	if err != nil || !safe.MatchString(key) {
		return fmt.Errorf("unsafe ids %q/%q", id, key)
	}
	if err := f.writeAtomic(filepath.Join(dir, "values", key+".json"), value); err != nil {
		return err
	}
	return f.Store.PutValue(id, key, value)
}

func (f *Store) DeleteSession(id string) error {
	dir, err := f.dir(id)
	if err != nil {
		return err
	}
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	return f.Store.DeleteSession(id)
}

func (f *Store) AppendRequest(r core.StoredRequest) error {
	data, err := jsonLines([]core.StoredRequest{r})
	if err != nil {
		return err
	}
	if err := f.appendFile(filepath.Join(f.root, "requests.jsonl"), data); err != nil {
		return err
	}
	return f.Store.AppendRequest(r)
}

func (f *Store) CompactRequests(keep []core.StoredRequest) error {
	data, err := jsonLines(keep)
	if err != nil {
		return err
	}
	if err := f.writeAtomic(filepath.Join(f.root, "requests.jsonl"), data); err != nil {
		return err
	}
	return f.Store.CompactRequests(keep)
}
