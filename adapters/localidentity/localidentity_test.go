package localidentity

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/siemendev/dzstudio-collab-server/core"
)

func TestTokensNamesAndCodes(t *testing.T) {
	now := int64(1_000_000)
	id, err := New(Options{
		Users:  []ConfiguredUser{{ID: "carla", Name: "Carla", Password: "pw"}, {ID: "kiosk", Name: "Kiosk"}},
		Secret: "0123456789abcdef0123456789abcdef", TokenTTLSeconds: 60,
		NameEnabled: true, CodeEnabled: true, CodeTTLSeconds: 600, CodeIntervalSecs: 2,
		Now: func() int64 { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	g, err := id.LoginWithName("  Anna ", core.LoginContext{})
	if err != nil {
		t.Fatal(err)
	}
	u, st := id.Authenticate(g.Token)
	if st != core.AuthOK || u.Name != "Anna" || u.UserID != NameUserID("Anna") {
		t.Fatalf("name login: %+v %v", u, st)
	}
	if _, err := id.LoginWithName("Carla", core.LoginContext{}); err == nil {
		t.Fatal("a name with a password logs in only by code")
	}
	if g, _ := id.LoginWithName("Kiosk", core.LoginContext{}); g.Token == "" {
		t.Fatal("a configured user without password logs in by name")
	} else if u, _ := id.Authenticate(g.Token); u.UserID != "kiosk" {
		t.Fatalf("configured id: %s", u.UserID)
	}
	if _, st := id.Authenticate(g.Token + "x"); st != core.AuthInvalid {
		t.Fatal("tampered token")
	}
	now += 61_000
	if _, st := id.Authenticate(g.Token); st != core.AuthExpired {
		t.Fatal("expired token")
	}

	start, _ := id.StartCodeLogin(core.LoginContext{RemoteAddress: "a", BaseURL: "http://x/dzcollab/"})
	if s, _ := id.PollCodeLogin(start.LoginCode); s.State != "waiting" {
		t.Fatal("waiting")
	}
	if id.Confirm(start.LoginCode, "carla", "wrong", false) != "bad_credentials" {
		t.Fatal("wrong password")
	}
	if id.Confirm(start.LoginCode[:4]+"-"+start.LoginCode[4:], "carla", "pw", false) != "ok" {
		t.Fatal("confirm with the shown form of the code")
	}
	s, _ := id.PollCodeLogin(start.LoginCode)
	if u, st := id.Authenticate(s.Grant.Token); s.State != "confirmed" || st != core.AuthOK || u.UserID != "carla" {
		t.Fatalf("confirmed: %+v", s)
	}
	if id.IsAgentToken(s.Grant.Token) {
		t.Fatal("a code login without kind agent gives a person's token")
	}

	agent, _ := id.StartCodeLogin(core.LoginContext{RemoteAddress: "b", BaseURL: "http://x/dzcollab/", Agent: true})
	w := httptest.NewRecorder()
	id.HandleHTTP(w, httptest.NewRequest("GET", "/dzcollab/auth/confirm?code="+agent.LoginCode, nil), "auth/confirm", nil, "b")
	if !strings.Contains(w.Body.String(), "AI agent") {
		t.Fatal("the confirmation page says that an agent is logging in")
	}
	id.Confirm(agent.LoginCode, "carla", "pw", false)
	as, _ := id.PollCodeLogin(agent.LoginCode)
	if u, st := id.Authenticate(as.Grant.Token); st != core.AuthOK || u.UserID != "carla" || !id.IsAgentToken(as.Grant.Token) {
		t.Fatalf("agent token: %+v %v", u, st)
	}
	if id.IsAgentToken(as.Grant.Token + "x") {
		t.Fatal("a tampered token is no agent token")
	}

	named, _ := id.StartCodeLogin(core.LoginContext{RemoteAddress: "c", BaseURL: "http://x/dzcollab/", Agent: true, AgentName: "<Scout>"})
	w = httptest.NewRecorder()
	id.HandleHTTP(w, httptest.NewRequest("GET", "/dzcollab/auth/confirm?code="+named.LoginCode, nil), "auth/confirm", nil, "c")
	if !strings.Contains(w.Body.String(), "“&lt;Scout&gt;”") {
		t.Fatal("the confirmation page names the agent, escaped")
	}
}
