package core_test

import (
	"strings"
	"testing"

	"github.com/siemendev/dzstudio-collab-server/adapters/fsprojects"
	"github.com/siemendev/dzstudio-collab-server/adapters/memstore"
	"github.com/siemendev/dzstudio-collab-server/core"
	p "github.com/siemendev/dzstudio-collab-server/protocol"
)

// agentTokens reads "anna" as Anna and "anna~agent" as a token of Anna bound to an agent.
type agentTokens struct{ tokens }

func (a agentTokens) Authenticate(t string) (core.User, core.AuthStatus) {
	return a.tokens.Authenticate(strings.TrimSuffix(t, "~agent"))
}
func (agentTokens) IsAgentToken(t string) bool { return strings.HasSuffix(t, "~agent") }

// noCarla gives everyone but Carla the rights of everyone{}; Carla comes in only by invitation.
type noCarla struct{ everyone }

func (n noCarla) Rights(u core.User, pc core.ProjectContext) (core.ProjectRights, error) {
	if u.UserID == "carla" {
		return core.ProjectRights{}, nil
	}
	return n.everyone.Rights(u, pc)
}

func newAgentHarness(t *testing.T, agents bool) *harness {
	h := &harness{t: t, clock: &clock{}}
	h.clock.ms.Store(1_000_000)
	h.storage = memstore.New()
	store := fsprojects.New("fs", t.TempDir(), "chernarus", h.clock.Now)
	svc, err := core.New(core.Options{ServerName: "t", Capabilities: core.Capabilities{Hosted: true, Agents: agents}},
		core.Deps{Identity: agentTokens{}, Permissions: noCarla{}, Storage: h.storage, Clock: h.clock, ProjectStores: []core.ProjectStore{store}})
	if err != nil || svc.Start() != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.Stop)
	h.svc = svc
	return h
}

func agentClient(kind string) map[string]any {
	c := map[string]any{"name": "dzstudio-mcp", "version": "0.1.0"}
	if kind != "" {
		c["kind"] = kind
	}
	return c
}

func (h *harness) join(token, sid, key string, c map[string]any) map[string]any {
	h.t.Helper()
	return h.ok("session/join", token, map[string]any{"requestId": rid(), "client": c, "sessionId": sid, "instanceKey": key})
}

func (h *harness) participants(token, sid, pid string) map[string]map[string]any {
	h.t.Helper()
	res := h.ok("poll", token, map[string]any{"sessionId": sid, "participantId": pid, "after": 0, "waitMs": 0})
	out := map[string]map[string]any{}
	for _, x := range res["participants"].(map[string]any)["list"].([]any) {
		m := x.(map[string]any)
		out[m["participantId"].(string)] = m
	}
	return out
}

func TestAgentIsMarkedAndHasNoRights(t *testing.T) {
	h := newAgentHarness(t, true)
	sid, pid := h.startLocal()
	j := h.join("anna", sid, "cccccccccc", map[string]any{"name": "dzstudio-mcp", "version": "0.1.0", "kind": "agent", "agentName": "Builder"})
	if j["canSave"] != false || j["canManage"] != false || j["canInvite"] != false {
		t.Fatalf("an agent of the host has no rights: %v", j)
	}
	apid := j["participantId"].(string)
	h.ok("poll", "anna", map[string]any{"sessionId": sid, "participantId": apid, "after": 0, "waitMs": 0,
		"presence": map[string]any{"idleSeconds": 0, "phase": "waiting"}})
	list := h.participants("anna", sid, pid)
	if list[apid]["kind"] != "agent" || list[apid]["name"] != "Anna" || list[apid]["agentName"] != "Builder" {
		t.Fatalf("agent entry: %v", list[apid])
	}
	if list[pid]["kind"] != "studio" || list[pid]["agentName"] != nil {
		t.Fatalf("host entry: %v", list[pid])
	}
	aids := map[string]any{"sessionId": sid, "participantId": apid}
	for endpoint, body := range map[string]map[string]any{
		"invite/create":      {"requestId": rid()},
		"locks/release":      {"requestId": rid(), "target": pid},
		"participant/remove": {"target": pid},
		"session/end":        {},
	} {
		for k, v := range aids {
			body[k] = v
		}
		if c := h.errCode(endpoint, "anna", body); c != p.NotAllowed {
			t.Fatalf("%s from an agent: %s", endpoint, c)
		}
	}
	h.restart2(t)
	if list := h.participants("anna", sid, pid); list[apid]["kind"] != "agent" || list[apid]["canManage"] != false {
		t.Fatalf("the kind survives a restart: %v", list[apid])
	}
}

// restart2 restarts a harness built by newAgentHarness on the same storage.
func (h *harness) restart2(t *testing.T) {
	h.svc.Stop()
	store := fsprojects.New("fs", t.TempDir(), "chernarus", h.clock.Now)
	svc, err := core.New(core.Options{ServerName: "t", Capabilities: core.Capabilities{Hosted: true, Agents: true}},
		core.Deps{Identity: agentTokens{}, Permissions: noCarla{}, Storage: h.storage, Clock: h.clock, ProjectStores: []core.ProjectStore{store}})
	if err != nil || svc.Start() != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.Stop)
	h.svc = svc
}

func TestAgentKindValidationAndResume(t *testing.T) {
	h := newAgentHarness(t, true)
	sid, _ := h.startLocal()
	body := map[string]any{"requestId": rid(), "client": agentClient("robot"), "sessionId": sid, "instanceKey": "cccccccccc"}
	if c := h.errCode("session/join", "ben", body); c != p.BadRequest {
		t.Fatalf("kind robot: %s", c)
	}
	plain := h.join("ben", sid, "dddddddddd", agentClient(""))
	if plain["canInvite"] != true {
		t.Fatalf("without kind a person: %v", plain)
	}
	a := h.join("ben", sid, "eeeeeeeeee", agentClient("agent"))
	resumed := h.ok("session/join", "ben", map[string]any{"requestId": rid(), "client": agentClient("studio"), "sessionId": sid,
		"instanceKey": "eeeeeeeeee", "resume": map[string]any{"participantId": a["participantId"], "after": 0}})
	if resumed["canInvite"] != false {
		t.Fatalf("an agent returning as studio stays an agent: %v", resumed)
	}
}

func TestAgentTokenIsAlwaysAnAgent(t *testing.T) {
	h := newAgentHarness(t, true)
	sid, _ := h.startLocal()
	j := h.join("ben~agent", sid, "cccccccccc", agentClient("studio"))
	if j["canInvite"] != false {
		t.Fatalf("a token bound to an agent makes an agent: %v", j)
	}
	for _, endpoint := range []string{"project/save", "session/end", "participant/remove", "locks/release", "invite/create"} {
		body := map[string]any{"requestId": rid(), "sessionId": "nope", "participantId": "p1", "target": "p2", "seq": 0, "files": []any{}}
		if c := h.errCode(endpoint, "ben~agent", body); c != p.NotAllowed {
			t.Fatalf("%s with an agent token on a session that does not exist: %s (rights before existence)", endpoint, c)
		}
	}
	local := map[string]any{"requestId": rid(), "client": agentClient(""), "projectName": "P", "map": "chernarus", "kind": "local",
		"instanceKey": "ffffffffff", "snapshotBytes": 2, "snapshotParts": 1}
	if c := h.errCode("session/create", "ben~agent", local); c != p.NotAllowed {
		t.Fatalf("an agent never hosts: %s", c)
	}
	hosted := map[string]any{"requestId": rid(), "client": agentClient("agent"), "projectName": "P", "map": "chernarus", "kind": "hosted",
		"hostedRef": map[string]any{"provider": "fs", "key": "abcdefghijklmnop"}, "hostedRevision": "", "instanceKey": "ffffffffff",
		"snapshotBytes": 2, "snapshotParts": 1}
	if c := h.errCode("session/create", "anna", hosted); c != p.NotAllowed {
		t.Fatalf("an agent creates no project: %s", c)
	}
}

func TestAgentPresence(t *testing.T) {
	h := newAgentHarness(t, true)
	sid, pid := h.startLocal()
	a := h.join("anna", sid, "cccccccccc", agentClient("agent"))["participantId"].(string)
	b := h.join("ben", sid, "dddddddddd", agentClient(""))["participantId"].(string)
	poll := func(token, who string, presence map[string]any) map[string]any {
		return h.call("poll", token, map[string]any{"sessionId": sid, "participantId": who, "after": 0, "waitMs": 0, "presence": presence})
	}
	for _, bad := range []map[string]any{
		{"idleSeconds": 0},
		{"idleSeconds": 0, "phase": "busy"},
		{"idleSeconds": 0, "phase": "working", "status": strings.Repeat("x", 201)},
		{"idleSeconds": 0, "phase": "working", "status": "two\nlines"},
	} {
		if res := poll("anna", a, bad); res["ok"] != false || res["error"].(map[string]any)["code"] != p.BadRequest {
			t.Fatalf("presence %v: %v", bad, res)
		}
	}
	status := strings.Repeat("ä", 100)
	if res := poll("anna", a, map[string]any{"idleSeconds": 0, "phase": "working", "status": status}); res["ok"] != true {
		t.Fatalf("200 bytes with umlauts: %v", res)
	}
	poll("ben", b, map[string]any{"idleSeconds": 0, "position": []any{1.0, 2.0, 3.0}, "phase": "working", "status": "x"})
	res := poll("anna", pid, map[string]any{"idleSeconds": 0, "position": []any{1.0, 2.0, 3.0}})
	for _, x := range res["presence"].([]any) {
		m := x.(map[string]any)
		switch m["participantId"] {
		case a:
			if m["phase"] != "working" || m["status"] != status || m["position"] != nil {
				t.Fatalf("agent presence: %v", m)
			}
		case b:
			if m["phase"] != nil || m["status"] != nil {
				t.Fatalf("phase and status of a person are not passed on: %v", m)
			}
		}
	}
}

func TestUserRemovesOwnAgentWithoutManaging(t *testing.T) {
	h := newAgentHarness(t, true)
	sid, pid := h.startLocal()
	code := h.ok("invite/create", "anna", map[string]any{"requestId": rid(), "sessionId": sid, "participantId": pid})["code"]
	h.ok("invite/redeem", "carla", map[string]any{"requestId": rid(), "code": code})
	c := h.join("carla", sid, "cccccccccc", agentClient(""))["participantId"].(string)
	ca := h.join("carla", sid, "dddddddddd", agentClient("agent"))["participantId"].(string)
	aa := h.join("anna", sid, "eeeeeeeeee", agentClient("agent"))["participantId"].(string)
	other := h.join("carla", sid, "ffffffffff", agentClient("agent"))["participantId"].(string)
	if code := h.errCode("participant/remove", "carla", map[string]any{"sessionId": sid, "participantId": c, "target": aa}); code != p.NotAllowed {
		t.Fatalf("a guest removes someone else's agent: %s", code)
	}
	if code := h.errCode("participant/remove", "carla", map[string]any{"sessionId": sid, "participantId": other, "target": ca}); code != p.NotAllowed {
		t.Fatalf("an agent removes its sibling: %s", code)
	}
	h.ok("participant/remove", "carla", map[string]any{"sessionId": sid, "participantId": c, "target": ca})
	res := h.call("poll", "carla", map[string]any{"sessionId": sid, "participantId": ca, "after": 0, "waitMs": 0})
	if e := res["error"].(map[string]any); e["code"] != p.NotParticipant || e["reason"] != "removed" {
		t.Fatalf("the removed agent: %v", e)
	}
	h.ok("session/leave", "carla", map[string]any{"sessionId": sid, "participantId": c})
	h.join("carla", sid, "gggggggggg", agentClient(""))
}

func TestWithoutCapabilityKindIsIgnored(t *testing.T) {
	h := newAgentHarness(t, false)
	sid, pid := h.startLocal()
	j := h.join("ben~agent", sid, "cccccccccc", agentClient("robot"))
	if j["canInvite"] != true {
		t.Fatalf("without the capability there are no agents: %v", j)
	}
	h.ok("poll", "ben~agent", map[string]any{"sessionId": sid, "participantId": j["participantId"], "after": 0, "waitMs": 0,
		"presence": map[string]any{"idleSeconds": 0, "phase": "busy"}})
	for _, m := range h.participants("anna", sid, pid) {
		if _, ok := m["kind"]; ok {
			t.Fatalf("no kind without the capability: %v", m)
		}
	}
	info := p.Serialize(h.svc.Dispatch("info", map[string]any{}, core.RequestContext{}))
	if strings.Contains(info, "agents") {
		t.Fatalf("info names agents: %s", info)
	}
}
