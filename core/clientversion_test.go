package core_test

import (
	"strings"
	"testing"

	"github.com/siemendev/dzstudio-collab-server/core"
	p "github.com/siemendev/dzstudio-collab-server/protocol"
)

const workshop = "https://steamcommunity.com/sharedfiles/filedetails/?id=1"

func client(version any) map[string]any {
	c := map[string]any{"name": "dzstudio"}
	if version != nil {
		c["version"] = version
	}
	return c
}

func create(h *harness, token string, c any) map[string]any {
	body := map[string]any{"requestId": rid(), "projectName": "P", "map": "chernarus", "kind": "local",
		"instanceKey": "aaaaaaaaaa", "snapshotBytes": 2, "snapshotParts": 1}
	if c != nil {
		body["client"] = c
	}
	return h.call("session/create", token, body)
}

func outdated(t *testing.T, res map[string]any, your string) {
	t.Helper()
	e, _ := res["error"].(map[string]any)
	if res["ok"] != false || e["code"] != p.ClientOutdated || e["minVersion"] != "0.4.0" || e["yourVersion"] != your || e["updateUrl"] != workshop {
		t.Fatalf("want client_outdated for %q: %v", your, res)
	}
}

func TestClientVersionBelowMinimumIsOutdated(t *testing.T) {
	h := newHarness(t)
	if err := h.svc.SetClientVersions(core.ClientVersions{Min: "0.4.0", Recommended: "0.5.0", UpdateURL: workshop}); err != nil {
		t.Fatal(err)
	}
	info := h.call("info", "", map[string]any{})
	if info["minClientVersion"] != "0.4.0" || info["recommendedClientVersion"] != "0.5.0" || info["updateUrl"] != workshop {
		t.Fatalf("info: %v", info)
	}
	for _, v := range []string{"0.3.9", "0.4", "0.4.0-beta", "00.4.0", ""} {
		outdated(t, create(h, "anna", client(v)), v)
	}
	outdated(t, create(h, "anna", client(nil)), "")
	outdated(t, create(h, "anna", client(400)), "")
	outdated(t, create(h, "anna", nil), "")
	outdated(t, create(h, "anna", client("0.3.9-with-a-very-long-suffix-over-32-bytes")), "0.3.9-with-a-very-long-suffix-ov")
	if res := create(h, "anna", client("0.3.9")); res["error"].(map[string]any)["message"] != "dzStudio 0.3.9 is out of date, 0.4.0 or newer is required." {
		t.Fatalf("message: %v", res)
	}
	outdated(t, create(h, "bad token.", client("0.3.9")), "0.3.9")
	raw := map[string]any{"protocol": "2.0", "token": "anna", "client": client("0.3.9")}
	if res := p.Serialize(h.svc.Dispatch("session/create", raw, core.RequestContext{})); !strings.Contains(res, p.ProtocolUnsupported) {
		t.Fatalf("protocol first: %s", res)
	}
	outdated(t, h.call("auth/name", "", map[string]any{"requestId": rid(), "name": "Anna", "client": client("0.3.9")}), "0.3.9")
	outdated(t, h.call("auth/code/start", "", map[string]any{}), "")
	if c := h.errCode("auth/name", "", map[string]any{"requestId": rid(), "name": "Anna", "client": client("0.4.0")}); c != p.CapabilityMissing {
		t.Fatalf("a current version reaches the next check: %s", c)
	}
	for _, v := range []string{"0.4.0", "0.10.0"} {
		if res := create(h, "anna", client(v)); res["ok"] != true {
			t.Fatalf("%s: %v", v, res)
		}
	}
	if res := create(h, "ben", map[string]any{"name": "other-client", "version": "7"}); res["ok"] != true {
		t.Fatalf("other client names are up to the server: %v", res)
	}
}

func TestRaisedMinimumNeverEndsARunningSession(t *testing.T) {
	h := newHarness(t)
	sid, _ := h.startLocal()
	join := h.ok("session/join", "ben", map[string]any{"requestId": rid(), "client": client("0.4.0"), "sessionId": sid, "instanceKey": "bbbbbbbbbb"})
	bpid := join["participantId"].(string)
	if err := h.svc.SetClientVersions(core.ClientVersions{Min: "0.5.0"}); err != nil {
		t.Fatal(err)
	}
	h.ok("poll", "ben", map[string]any{"sessionId": sid, "participantId": bpid, "after": 0, "waitMs": 0})
	h.ok("session/join", "ben", map[string]any{"requestId": rid(), "client": client("0.4.0"), "sessionId": sid, "instanceKey": "bbbbbbbbbb",
		"resume": map[string]any{"participantId": bpid, "after": 0}})
	if c := h.errCode("session/join", "carl", map[string]any{"requestId": rid(), "client": client("0.4.0"), "sessionId": sid, "instanceKey": "cccccccccc"}); c != p.ClientOutdated {
		t.Fatalf("a new join: %s", c)
	}
	if err := h.svc.SetClientVersions(core.ClientVersions{}); err != nil {
		t.Fatal(err)
	}
	h.ok("session/join", "carl", map[string]any{"requestId": rid(), "client": client("0.0.1"), "sessionId": sid, "instanceKey": "cccccccccc"})
}

func TestNoMinimumNoFields(t *testing.T) {
	h := newHarness(t)
	if err := h.svc.SetClientVersions(core.ClientVersions{Recommended: "0.5.0", UpdateURL: workshop}); err != nil {
		t.Fatal(err)
	}
	info := h.call("info", "", map[string]any{})
	for _, k := range []string{"minClientVersion", "recommendedClientVersion", "updateUrl"} {
		if _, ok := info[k]; ok {
			t.Fatalf("info names %s without a minimum", k)
		}
	}
	if res := create(h, "anna", client("conformance")); res["ok"] != true {
		t.Fatalf("no check: %v", res)
	}
	if err := h.svc.SetClientVersions(core.ClientVersions{Min: "0.5.0", Recommended: "0.5.0"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := h.call("info", "", map[string]any{})["recommendedClientVersion"]; ok {
		t.Fatal("a recommendation not above the minimum counts as none")
	}
	for _, bad := range []core.ClientVersions{{Min: "0.4"}, {Min: "0.4.0", Recommended: "x"}, {Min: "0.4.0", UpdateURL: "ftp://x"}} {
		if h.svc.SetClientVersions(bad) == nil {
			t.Fatalf("%v should be refused", bad)
		}
	}
}
