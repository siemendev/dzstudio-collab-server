package core_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/siemendev/dzstudio-collab-server/adapters/fsprojects"
	"github.com/siemendev/dzstudio-collab-server/adapters/memstore"
	"github.com/siemendev/dzstudio-collab-server/core"
)

// deferred is a memstore whose writes become durable only when the test says so.
type deferred struct {
	*memstore.Store
	mu      sync.Mutex
	writes  uint64
	durable uint64
	failed  error
	changed chan struct{}
}

func (d *deferred) bump() {
	d.mu.Lock()
	d.writes++
	d.mu.Unlock()
}
func (d *deferred) Append(id string, e []core.LogEntry) error { d.bump(); return d.Store.Append(id, e) }
func (d *deferred) SaveRecord(id string, r []byte) error      { d.bump(); return d.Store.SaveRecord(id, r) }
func (d *deferred) Mark() uint64 {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.writes
}
func (d *deferred) WaitDurable(ctx context.Context, mark uint64) error {
	for {
		d.mu.Lock()
		done, failed, ch := d.durable >= mark, d.failed, d.changed
		d.mu.Unlock()
		if failed != nil {
			return failed
		}
		if done {
			return nil
		}
		select {
		case <-ch:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}
func (d *deferred) release(fail error) {
	d.mu.Lock()
	d.durable, d.failed = d.writes, fail
	close(d.changed)
	d.changed = make(chan struct{})
	d.mu.Unlock()
}

func TestAnAnswerWaitsUntilItsWritesAreDurable(t *testing.T) {
	d := &deferred{Store: memstore.New(), changed: make(chan struct{})}
	svc, err := core.New(core.Options{ServerName: "t"}, core.Deps{Identity: tokens{}, Permissions: everyone{}, Storage: d, Clock: &clock{}})
	if err != nil || svc.Start() != nil {
		t.Fatal(err)
	}
	defer svc.Stop()
	h := &harness{t: t, svc: svc}
	done := make(chan map[string]any, 1)
	go func() {
		done <- h.call("session/create", "anna", map[string]any{
			"requestId": rid(), "client": map[string]any{"name": "dzstudio", "version": "t"}, "projectName": "P", "map": "m",
			"kind": "local", "instanceKey": "aaaaaaaaaa", "snapshotBytes": 2, "snapshotParts": 1,
		})
	}()
	select {
	case <-done:
		t.Fatal("answered before its writes were durable")
	default:
	}
	for d.Mark() == 0 {
	}
	d.release(nil)
	if res := <-done; res["ok"] != true {
		t.Fatalf("%v", res)
	}
	d.release(errors.New("lost"))
	res := h.call("session/create", "anna", map[string]any{
		"requestId": rid(), "client": map[string]any{"name": "dzstudio", "version": "t"}, "projectName": "P", "map": "m",
		"kind": "local", "instanceKey": "bbbbbbbbbb", "snapshotBytes": 2, "snapshotParts": 1,
	})
	if res["ok"] != false || res["error"].(map[string]any)["code"] != "busy" {
		t.Fatalf("a lost write must not be answered as done: %v", res)
	}
}

func TestSessionsPerUserAreLimited(t *testing.T) {
	h := &harness{t: t, storage: memstore.New(), clock: &clock{}}
	svc, _ := core.New(core.Options{ServerName: "t", Limits: core.Limits{MaxLocalSessionsPerUser: 2}},
		core.Deps{Identity: tokens{}, Permissions: everyone{}, Storage: h.storage, Clock: h.clock})
	_ = svc.Start()
	defer svc.Stop()
	h.svc = svc
	sid, pid := h.startLocal()
	h.startLocal()
	body := map[string]any{"requestId": rid(), "client": map[string]any{"name": "dzstudio", "version": "t"}, "projectName": "P", "map": "m",
		"kind": "local", "instanceKey": "cccccccccc", "snapshotBytes": 2, "snapshotParts": 1}
	if code := h.errCode("session/create", "anna", body); code != "not_allowed" {
		t.Fatalf("third session: %s", code)
	}
	h.ok("session/create", "ben", map[string]any{"requestId": rid(), "client": map[string]any{"name": "dzstudio", "version": "t"}, "projectName": "P",
		"map": "m", "kind": "local", "instanceKey": "dddddddddd", "snapshotBytes": 2, "snapshotParts": 1})
	h.ok("session/end", "anna", map[string]any{"sessionId": sid, "participantId": pid})
	body["requestId"] = rid()
	h.ok("session/create", "anna", body)
}

func TestTheOperatorEndsIdleSessionsAndTheIdleClockSurvivesARestart(t *testing.T) {
	h := &harness{t: t, storage: memstore.New(), clock: &clock{}}
	h.clock.ms.Store(1_000_000)
	store := fsprojects.New("fs", t.TempDir(), "chernarus", h.clock.Now)
	start := func() {
		svc, err := core.New(core.Options{ServerName: "t", Capabilities: core.Capabilities{Hosted: true}},
			core.Deps{Identity: tokens{}, Permissions: everyone{}, Storage: h.storage, Clock: h.clock, ProjectStores: []core.ProjectStore{store}})
		if err != nil || svc.Start() != nil {
			t.Fatal(err)
		}
		h.svc = svc
	}
	start()
	hosted := func(key string) (string, string) {
		res := h.ok("session/create", "anna", map[string]any{
			"requestId": rid(), "client": map[string]any{"name": "dzstudio", "version": "t"}, "projectName": "P", "map": "m", "kind": "hosted",
			"hostedRef": map[string]any{"provider": "fs", "key": key}, "hostedRevision": "", "instanceKey": "aaaaaaaaaa", "snapshotBytes": 2, "snapshotParts": 1,
		})
		sid, pid := res["sessionId"].(string), res["participantId"].(string)
		h.ok("snapshot/put", "anna", map[string]any{"sessionId": sid, "participantId": pid, "seq": 0, "part": 0, "parts": 1, "pieces": []any{"{}"}})
		h.ok("ops/send", "anna", map[string]any{"sessionId": sid, "participantId": pid, "ops": []any{op(1, "k-1", nil)}})
		return sid, pid
	}
	day := int64(24 * 3600_000)
	sid, pid := hosted("a")
	h.ok("session/leave", "anna", map[string]any{"sessionId": sid, "participantId": pid})
	h.clock.ms.Add(20 * day)
	h.svc.Tick()
	h.svc.Stop()
	start()
	defer h.svc.Stop()
	h.clock.ms.Add(11 * day)
	if ended := h.svc.EndIdle(30 * day); len(ended) != 1 || ended[0] != sid {
		t.Fatalf("ended %v", ended)
	}
	sid2, pid2 := hosted("b")
	h.clock.ms.Add(29 * day)
	h.ok("poll", "anna", map[string]any{"sessionId": sid2, "participantId": pid2, "after": 0, "waitMs": 0})
	h.clock.ms.Add(2 * day)
	if ended := h.svc.EndIdle(30 * day); len(ended) != 0 {
		t.Fatalf("a session used two days ago was ended: %v", ended)
	}
}

type creators struct{ everyone }

func (creators) MayCreate(u core.User, provider string) (bool, error) {
	return provider == "fs" && u.UserID == "anna", nil
}

func TestTheFirstSaveOfANewProjectWritesAtSeqZero(t *testing.T) {
	h := &harness{t: t, storage: memstore.New(), clock: &clock{}}
	h.clock.ms.Store(1_000_000)
	store := fsprojects.New("fs", t.TempDir(), "chernarus", h.clock.Now)
	svc, err := core.New(core.Options{ServerName: "t", Capabilities: core.Capabilities{Hosted: true}},
		core.Deps{Identity: tokens{}, Permissions: creators{}, Storage: h.storage, Clock: h.clock, ProjectStores: []core.ProjectStore{store}})
	if err != nil || svc.Start() != nil {
		t.Fatal(err)
	}
	h.svc = svc
	defer svc.Stop()

	for user, want := range map[string]bool{"anna": true, "ben": false} {
		stores := h.ok("project/list", user, map[string]any{})["stores"].([]any)
		if len(stores) != 1 || stores[0].(map[string]any)["provider"] != "fs" || stores[0].(map[string]any)["canCreate"] != want {
			t.Fatalf("%s: stores %v", user, stores)
		}
	}

	ref := map[string]any{"provider": "fs", "key": "k7qm2x9a0bcd4efg"}
	res := h.ok("session/create", "anna", map[string]any{
		"requestId": rid(), "client": map[string]any{"name": "dzstudio", "version": "t"}, "projectName": "Server Base", "map": "chernarus",
		"kind": "hosted", "hostedRef": ref, "hostedRevision": "", "instanceKey": "aaaaaaaaaa", "snapshotBytes": 2, "snapshotParts": 1,
	})
	ids := map[string]any{"sessionId": res["sessionId"], "participantId": res["participantId"]}
	h.ok("snapshot/put", "anna", map[string]any{"sessionId": ids["sessionId"], "participantId": ids["participantId"], "seq": 0, "part": 0, "parts": 1, "pieces": []any{"{}"}})
	save := func() map[string]any {
		return h.ok("project/save", "anna", map[string]any{"requestId": rid(), "sessionId": ids["sessionId"], "participantId": ids["participantId"], "seq": 0,
			"files": []any{map[string]any{"name": "f-base", "pieces": []any{"{}"}, "checksum": "44136fa355b3678a1146ad16f7e8649e94fb4fc21fe77e8310c060f61caaff8a"}}})
	}
	first := save()
	if first["superseded"] != false || first["savedSeq"] != float64(0) || first["hostedRevision"] == "" {
		t.Fatalf("first save at seq 0: %v", first)
	}
	head := h.ok("project/revision", "anna", map[string]any{"hostedRef": ref})
	if head["hostedRevision"] != first["hostedRevision"] || head["projectName"] != "Server Base" {
		t.Fatalf("store after the first save: %v", head)
	}
	saving := h.ok("poll", "anna", map[string]any{"sessionId": ids["sessionId"], "participantId": ids["participantId"], "after": 0, "waitMs": 0})["saving"].(map[string]any)
	if saving["savedBy"] == nil {
		t.Fatalf("saving after the first save: %v", saving)
	}
	if again := save(); again["superseded"] != true || again["hostedRevision"] != first["hostedRevision"] {
		t.Fatalf("second save at seq 0: %v", again)
	}
}
