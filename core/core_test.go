package core_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/siemendev/dzstudio-collab-server/adapters/memstore"
	"github.com/siemendev/dzstudio-collab-server/core"
	p "github.com/siemendev/dzstudio-collab-server/protocol"
)

type clock struct{ ms atomic.Int64 }

func (c *clock) Now() int64 { return c.ms.Load() }

type tokens struct{}

func (tokens) AuthMethods() []core.AuthMethod {
	return []core.AuthMethod{{Method: "provider", Provider: "test"}}
}
func (tokens) Authenticate(t string) (core.User, core.AuthStatus) {
	if t == "" || strings.ContainsAny(t, " .") {
		return core.User{}, core.AuthInvalid
	}
	return core.User{UserID: t, Name: strings.ToUpper(t[:1]) + t[1:]}, core.AuthOK
}

type everyone struct{}

func (everyone) MayHostLocal(core.User) (bool, error) { return true, nil }
func (everyone) Rights(u core.User, pc core.ProjectContext) (core.ProjectRights, error) {
	return core.ProjectRights{Join: true, Save: true, Manage: u.UserID == "anna", Invite: true}, nil
}

type harness struct {
	t       *testing.T
	storage *memstore.Store
	clock   *clock
	svc     *core.Service
}

func newHarness(t *testing.T) *harness {
	h := &harness{t: t, storage: memstore.New(), clock: &clock{}}
	h.clock.ms.Store(1_000_000)
	h.start()
	return h
}

func (h *harness) start() {
	svc, err := core.New(core.Options{ServerName: "test", Limits: core.Limits{CheckpointOps: 1_000_000}},
		core.Deps{Identity: tokens{}, Permissions: everyone{}, Storage: h.storage, Clock: h.clock})
	if err != nil {
		h.t.Fatal(err)
	}
	if err := svc.Start(); err != nil {
		h.t.Fatal(err)
	}
	h.svc = svc
	h.t.Cleanup(svc.Stop)
}

func (h *harness) restart() {
	h.svc.Stop()
	h.start()
}

// call sends a request as JSON text, exactly like the HTTP layer does, and decodes the answer.
func (h *harness) call(endpoint, token string, body map[string]any) map[string]any {
	h.t.Helper()
	body["protocol"] = "1.0"
	body["token"] = token
	data, _ := json.Marshal(body)
	m, err := p.ParseBody(data)
	if err != nil {
		h.t.Fatal(err)
	}
	out := p.Serialize(h.svc.Dispatch(endpoint, m, core.RequestContext{RemoteAddress: "test", BaseURL: "http://test/"}))
	var res map[string]any
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		h.t.Fatalf("answer is not JSON: %s", out)
	}
	return res
}

func (h *harness) ok(endpoint, token string, body map[string]any) map[string]any {
	h.t.Helper()
	res := h.call(endpoint, token, body)
	if res["ok"] != true {
		h.t.Fatalf("%s: %v", endpoint, res["error"])
	}
	return res
}

func (h *harness) errCode(endpoint, token string, body map[string]any) string {
	h.t.Helper()
	res := h.call(endpoint, token, body)
	if res["ok"] != false {
		h.t.Fatalf("%s should fail: %v", endpoint, res)
	}
	return res["error"].(map[string]any)["code"].(string)
}

var ridN atomic.Int64

func rid() string { return fmt.Sprintf("%016d", ridN.Add(1)) }

// startLocal creates a local session for anna with a one-part snapshot.
func (h *harness) startLocal() (string, string) {
	res := h.ok("session/create", "anna", map[string]any{
		"requestId": rid(), "client": map[string]any{"name": "dzstudio", "version": "t"}, "projectName": "P", "map": "chernarus",
		"kind": "local", "instanceKey": "aaaaaaaaaa", "snapshotBytes": 2, "snapshotParts": 1,
	})
	sid, pid := res["sessionId"].(string), res["participantId"].(string)
	h.ok("snapshot/put", "anna", map[string]any{"sessionId": sid, "participantId": pid, "seq": 0, "part": 0, "parts": 1, "pieces": []any{"{}"}})
	return sid, pid
}

func op(clientSeq int, opID string, extra map[string]any) map[string]any {
	o := map[string]any{"clientSeq": clientSeq, "opId": opID, "pieces": []any{`{"v":1}`}}
	for k, v := range extra {
		o[k] = v
	}
	return o
}

func num(v any) int64 { return int64(v.(float64)) }

func TestChainAppendedAfterLastLinkAndSurvivesRestart(t *testing.T) {
	h := newHarness(t)
	sid, pid := h.startLocal()
	ids := func(ops ...any) map[string]any {
		return map[string]any{"sessionId": sid, "participantId": pid, "ops": ops}
	}
	link := func(cs, i int) map[string]any {
		return op(cs, "k-1", map[string]any{"chain": map[string]any{"index": i, "count": 3}})
	}

	r := h.ok("ops/send", "anna", ids(link(1, 0)))
	if len(r["results"].([]any)) != 0 || num(r["nextClientSeq"]) != 2 {
		t.Fatalf("first link: %v", r)
	}
	if c := h.errCode("ops/send", "anna", ids(op(2, "x-1", nil))); c != p.BatchPending {
		t.Fatalf("op while chain open: %s", c)
	}
	if c := h.errCode("ops/send", "anna", ids(op(2, "k-1", map[string]any{"chain": map[string]any{"index": 2, "count": 3}}))); c != p.BadRequest {
		t.Fatalf("skipped index: %s", c)
	}
	h.ok("ops/send", "anna", ids(link(2, 1)))
	h.restart()
	r = h.ok("ops/send", "anna", ids(link(2, 1), link(3, 2)))
	res := r["results"].([]any)
	if len(res) != 3 {
		t.Fatalf("the answer with the last link names every link: %v", res)
	}
	for i, x := range res {
		if num(x.(map[string]any)["seq"]) != int64(i+1) {
			t.Fatalf("links consecutive from 1: %v", res)
		}
	}
	poll := h.ok("poll", "anna", map[string]any{"sessionId": sid, "participantId": pid, "after": 0, "waitMs": 0})
	ops := poll["ops"].([]any)
	if len(ops) != 3 || ops[2].(map[string]any)["chain"].(map[string]any)["index"] != float64(2) {
		t.Fatalf("poll: %v", ops)
	}
}

func TestOperationOverMaxOpBytesIsRefusedAndForgotten(t *testing.T) {
	h := newHarness(t)
	sid, pid := h.startLocal()
	piece := strings.Repeat("x", 998)
	part := make([]any, 1040)
	for i := range part {
		part[i] = piece
	}
	items := []any{}
	for i := 0; i < 4; i++ {
		items = append(items, map[string]any{"clientSeq": 1, "opId": "big-1", "part": i, "parts": 4, "pieces": part})
	}
	res := h.call("ops/send", "anna", map[string]any{"sessionId": sid, "participantId": pid, "ops": items})
	e := res["error"].(map[string]any)
	if e["code"] != p.TooLarge || num(e["maxBytes"]) != core.MaxOpBytes {
		t.Fatalf("too_large expected: %v", e)
	}
	r := h.ok("ops/send", "anna", map[string]any{"sessionId": sid, "participantId": pid, "ops": []any{op(1, "small-1", nil)}})
	if num(r["results"].([]any)[0].(map[string]any)["seq"]) != 1 {
		t.Fatalf("nothing of the refused operation is in the log: %v", r)
	}
}

func TestRetryWindowAndVersionsSurviveRestart(t *testing.T) {
	h := newHarness(t)
	sid, pid := h.startLocal()
	body := map[string]any{"requestId": "abcdefghij012345", "sessionId": sid, "participantId": pid}
	first := h.ok("invite/create", "anna", body)
	poll := h.ok("poll", "anna", map[string]any{"sessionId": sid, "participantId": pid, "after": 0, "waitMs": 0})
	before := num(poll["participants"].(map[string]any)["version"])
	lock := h.ok("locks/set", "anna", map[string]any{"sessionId": sid, "participantId": pid, "lockSeq": 3, "elements": []any{"e1"}})
	h.restart()
	again := h.ok("invite/create", "anna", map[string]any{"requestId": "abcdefghij012345", "sessionId": sid, "participantId": pid})
	if again["code"] != first["code"] {
		t.Fatalf("the repetition after a restart gets the same code: %v vs %v", again["code"], first["code"])
	}
	lockAgain := h.ok("locks/set", "anna", map[string]any{"sessionId": sid, "participantId": pid, "lockSeq": 3, "elements": []any{"e2"}})
	if fmt.Sprint(lockAgain["granted"]) != fmt.Sprint(lock["granted"]) || lockAgain["version"] != lock["version"] {
		t.Fatalf("locks/set repetition after a restart: %v vs %v", lockAgain, lock)
	}
	versions := map[string]any{"participants": before, "locks": before, "live": before, "notices": before, "saving": before}
	poll = h.ok("poll", "anna", map[string]any{"sessionId": sid, "participantId": pid, "after": 0, "waitMs": 0, "versions": versions})
	for _, k := range []string{"participants", "locks", "live", "notices", "saving"} {
		v, ok := poll[k].(map[string]any)
		if !ok || num(v["version"]) <= before {
			t.Fatalf("%s after the restart: %v", k, poll[k])
		}
	}
}

func TestPollLeavesOutSideFieldsWhenTheFirstOperationWouldNotFit(t *testing.T) {
	h := newHarness(t)
	sid, pid := h.startLocal()
	ids := map[string]any{"sessionId": sid, "participantId": pid}
	els := make([]any, 20_000)
	for i := range els {
		els[i] = fmt.Sprintf("%064d", i)
	}
	h.ok("locks/set", "anna", map[string]any{"sessionId": sid, "participantId": pid, "lockSeq": 1, "elements": els})
	piece := strings.Repeat("y", 998)
	part := make([]any, 1045)
	for i := range part {
		part[i] = piece
	}
	var items []any
	for i := 0; i < 3; i++ {
		items = append(items, map[string]any{"clientSeq": 1, "opId": "big-1", "part": i, "parts": 3, "pieces": part})
	}
	h.ok("ops/send", "anna", map[string]any{"sessionId": sid, "participantId": pid, "ops": items})
	body := map[string]any{"after": 0, "waitMs": 0, "maxBytes": core.ResponseBytes}
	for k, v := range ids {
		body[k] = v
	}
	body["protocol"], body["token"] = "1.0", "anna"
	data, _ := json.Marshal(body)
	m, _ := p.ParseBody(data)
	ans := h.svc.Dispatch("poll", m, core.RequestContext{})
	size := len(p.Encode(ans)) - len(p.Marker)
	if size > core.ResponseBytes {
		t.Fatalf("answer %d bytes", size)
	}
	if more, _ := ans.Get("more"); ans.Has("locks") || more != true {
		t.Fatalf("locks left out and more true expected (locks present %v)", ans.Has("locks"))
	}
	next := h.ok("poll", "anna", map[string]any{"sessionId": sid, "participantId": pid, "after": 1, "waitMs": 0})
	if _, ok := next["locks"]; !ok {
		t.Fatal("the next answer brings locks")
	}
}

func TestLeaveThenAnyRequestIsNotParticipantTimeout(t *testing.T) {
	h := newHarness(t)
	sid, pid := h.startLocal()
	join := h.ok("session/join", "ben", map[string]any{"requestId": rid(), "client": map[string]any{"name": "dzstudio", "version": "t"}, "sessionId": sid, "instanceKey": "bbbbbbbbbb"})
	bpid := join["participantId"].(string)
	ids := map[string]any{"sessionId": sid, "participantId": bpid}
	h.ok("session/leave", "ben", ids)
	h.ok("session/leave", "ben", map[string]any{"sessionId": sid, "participantId": bpid})
	res := h.call("poll", "ben", map[string]any{"sessionId": sid, "participantId": bpid, "after": 0, "waitMs": 0})
	e := res["error"].(map[string]any)
	if e["code"] != p.NotParticipant || e["reason"] != "timeout" {
		t.Fatalf("after leave: %v", e)
	}
	_ = pid
}

func TestCreatorWithoutContactBeforeSnapshotZeroDiscardsTheSession(t *testing.T) {
	h := newHarness(t)
	res := h.ok("session/create", "anna", map[string]any{
		"requestId": rid(), "client": map[string]any{"name": "dzstudio", "version": "t"}, "projectName": "P", "map": "chernarus",
		"kind": "local", "instanceKey": "aaaaaaaaaa", "snapshotBytes": 2, "snapshotParts": 2,
	})
	sid := res["sessionId"].(string)
	h.clock.ms.Add(300_001)
	h.svc.Tick()
	if c := h.errCode("session/join", "ben", map[string]any{"requestId": rid(), "client": map[string]any{"name": "dzstudio", "version": "t"}, "sessionId": sid, "instanceKey": "bbbbbbbbbb"}); c != p.SessionUnknown {
		t.Fatalf("join after the creator was gone 300 s: %s", c)
	}
}
