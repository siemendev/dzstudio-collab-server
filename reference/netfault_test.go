package reference

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/siemendev/dzstudio-collab-server/config"
)

type netHarness struct {
	t   *testing.T
	url string
}

func newNetHarness(t *testing.T) *netHarness {
	cfg := config.Default()
	cfg.DataDir = t.TempDir()
	cfg.Storage.Type = "memory"
	cfg.Testing.Enabled = true
	cfg.Auth.Secret = "net-fault-test-secret-net-fault-test-secret"
	srv, err := New(cfg, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.Service.Start(); err != nil {
		t.Fatal(err)
	}
	hs := httptest.NewServer(srv.Handler)
	t.Cleanup(hs.Close)
	return &netHarness{t, hs.URL + "/dzcollab/"}
}

// post returns the answer object, or nil when the connection closed without one.
func (h *netHarness) post(endpoint string, body map[string]any) map[string]any {
	h.t.Helper()
	data, _ := json.Marshal(body)
	client := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}
	res, err := client.Post(h.url+endpoint, "application/json", bytes.NewReader(data))
	if err != nil {
		return nil
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		return nil
	}
	line, rest, _ := strings.Cut(string(raw), "\n")
	if line != "dzcollab 1" {
		h.t.Fatalf("%s: no marker line: %q", endpoint, raw)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(rest), &out); err != nil {
		h.t.Fatalf("%s: %v", endpoint, err)
	}
	return out
}

func (h *netHarness) ok(endpoint string, body map[string]any) map[string]any {
	h.t.Helper()
	out := h.post(endpoint, body)
	if out == nil || out["ok"] != true {
		h.t.Fatalf("%s: %v", endpoint, out)
	}
	return out
}

func (h *netHarness) login(name string) string {
	return h.ok("auth/name", map[string]any{"protocol": "1.0", "requestId": "login" + strings.Repeat("0", 11), "name": name})["token"].(string)
}

func createBody(token, requestID string) map[string]any {
	return map[string]any{"protocol": "1.0", "token": token, "requestId": requestID,
		"client": map[string]any{"name": "dzstudio", "version": "t"}, "projectName": "P", "map": "chernarus",
		"kind": "local", "instanceKey": "aaaaaaaaaa", "snapshotBytes": 2, "snapshotParts": 1}
}

func TestNetRefuseClosesWithoutProcessing(t *testing.T) {
	h := newNetHarness(t)
	anna := h.login("Anna")
	cr := h.ok("session/create", createBody(anna, "r000000000000001"))
	put := map[string]any{"protocol": "1.0", "token": anna, "sessionId": cr["sessionId"], "participantId": cr["participantId"],
		"seq": 0, "part": 0, "parts": 1, "pieces": []string{"{}"}}
	h.ok("_test/net", map[string]any{"refuse": true})
	if out := h.post("info", map[string]any{}); out != nil {
		t.Fatalf("refused request answered: %v", out)
	}
	if out := h.post("snapshot/put", put); out != nil {
		t.Fatalf("refused request answered: %v", out)
	}
	h.ok("_test/net", map[string]any{})
	list := h.ok("sessions/list", map[string]any{"protocol": "1.0", "token": anna})
	if n := len(list["sessions"].([]any)); n != 0 {
		t.Fatalf("refused create was processed: %v", list)
	}
	h.ok("info", map[string]any{})
}

func TestNetDropAppliesButLosesTheAnswerForOneUser(t *testing.T) {
	h := newNetHarness(t)
	anna, ben := h.login("Anna"), h.login("Ben")
	h.ok("_test/net", map[string]any{"userId": "name:Anna", "dropResponses": true})
	if out := h.post("session/create", createBody(anna, "r000000000000002")); out != nil {
		t.Fatalf("dropped answer arrived: %v", out)
	}
	h.ok("sessions/list", map[string]any{"protocol": "1.0", "token": ben})
	h.ok("_test/net", map[string]any{})
	retry := h.ok("session/create", createBody(anna, "r000000000000002"))
	again := h.ok("session/create", createBody(anna, "r000000000000002"))
	if retry["sessionId"] != again["sessionId"] || retry["participantId"] != "p1" {
		t.Fatalf("retry after a dropped answer is not the applied request: %v / %v", retry, again)
	}
}

func TestNetDelayAndUntil(t *testing.T) {
	h := newNetHarness(t)
	set := h.ok("_test/net", map[string]any{"delayMs": 300})
	start := time.Now()
	h.ok("info", map[string]any{})
	if d := time.Since(start); d < 300*time.Millisecond {
		t.Fatalf("answer not delayed: %v", d)
	}
	now := int64(set["nowMs"].(float64))
	h.ok("_test/net", map[string]any{"refuse": true, "untilMs": now + 60_000})
	if h.post("info", map[string]any{}) != nil {
		t.Fatal("refuse not active")
	}
	h.ok("_test/clock", map[string]any{"advanceMs": 61_000})
	h.ok("info", map[string]any{})
	if out := h.post("_test/net", map[string]any{"delayMs": -1}); out == nil || out["ok"] != false {
		t.Fatalf("bad delay accepted: %v", out)
	}
}
