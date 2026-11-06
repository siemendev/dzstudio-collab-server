// Package localidentity is the self-hosting identity: users from the config file, login by display name
// (auth.name, for servers in your own network) and/or by a login code confirmed with user name and password on a
// page of this server (auth.code). Tokens are signed (HMAC-SHA256) and survive restarts while the secret stays.
package localidentity

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"html"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"

	"github.com/siemendev/dzstudio-collab-server/core"
	p "github.com/siemendev/dzstudio-collab-server/protocol"
)

type ConfiguredUser struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Password string `json:"password,omitempty"`
}

type Options struct {
	Users            []ConfiguredUser
	Secret           string
	TokenTTLSeconds  int64
	NameEnabled      bool
	AllowedNames     []string
	CodeEnabled      bool
	CodeTTLSeconds   int64
	CodeIntervalSecs int64
	Now              func() int64
}

type pending struct {
	expiresAt int64
	state     string
	grant     core.TokenGrant
	agent     bool
}

type Identity struct {
	opts Options

	mu       sync.Mutex
	pending  map[string]*pending
	attempts map[string][]int64
	seen     map[string]seenUser
}

type seenUser struct {
	user core.User
	at   int64
}

func New(opts Options) (*Identity, error) {
	if len(opts.Secret) < 32 {
		return nil, errors.New("auth secret must have at least 32 characters")
	}
	return &Identity{opts: opts, pending: map[string]*pending{}, attempts: map[string][]int64{}, seen: map[string]seenUser{}}, nil
}

func (id *Identity) now() int64 { return id.opts.Now() }

func (id *Identity) AuthMethods() []core.AuthMethod {
	var out []core.AuthMethod
	if id.opts.CodeEnabled {
		out = append(out, core.AuthMethod{Method: "code"})
	}
	if id.opts.NameEnabled {
		out = append(out, core.AuthMethod{Method: "name"})
	}
	return out
}

// ConfiguredUsers are the users from the config file.
func (id *Identity) ConfiguredUsers() []core.User {
	out := make([]core.User, len(id.opts.Users))
	for i, u := range id.opts.Users {
		out[i] = core.User{UserID: u.ID, Name: u.Name}
	}
	return out
}

// Seen is everyone who authenticated since since (ms).
func (id *Identity) Seen(since int64) []core.User {
	id.mu.Lock()
	defer id.mu.Unlock()
	var out []core.User
	for _, s := range id.seen {
		if s.at >= since {
			out = append(out, s.user)
		}
	}
	return out
}

// ------------------------------------------------------------ tokens

type payload struct {
	U string `json:"u"`
	N string `json:"n"`
	K string `json:"k"`
	E int64  `json:"e"`
	A bool   `json:"a,omitempty"`
}

var b64 = base64.RawURLEncoding

func (id *Identity) sign(s string) string {
	m := hmac.New(sha256.New, []byte(id.opts.Secret))
	m.Write([]byte(s))
	return b64.EncodeToString(m.Sum(nil))
}

func (id *Identity) issue(u core.User, kind string, agent bool) core.TokenGrant {
	exp := id.now()/1000 + id.opts.TokenTTLSeconds
	data, _ := json.Marshal(payload{U: u.UserID, N: u.Name, K: kind, E: exp, A: agent})
	body := b64.EncodeToString(data)
	return core.TokenGrant{Token: "v1." + body + "." + id.sign(body), Name: u.Name, TokenExpiresInSeconds: id.opts.TokenTTLSeconds}
}

func (id *Identity) Authenticate(token string) (core.User, core.AuthStatus) {
	u, st := id.Verify(token)
	if st == core.AuthOK {
		id.mu.Lock()
		id.seen[u.UserID] = seenUser{u, id.now()}
		id.mu.Unlock()
	}
	return u, st
}

// IsAgentToken: the token is valid and came from a code login that named client.kind "agent".
func (id *Identity) IsAgentToken(token string) bool {
	pl, st := id.verify(token)
	return st == core.AuthOK && pl.A
}

// Verify checks a token like Authenticate without counting it as contact.
func (id *Identity) Verify(token string) (core.User, core.AuthStatus) {
	pl, st := id.verify(token)
	if st != core.AuthOK {
		return core.User{}, st
	}
	return core.User{UserID: pl.U, Name: pl.N}, core.AuthOK
}

func (id *Identity) verify(token string) (payload, core.AuthStatus) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 || parts[0] != "v1" {
		return payload{}, core.AuthInvalid
	}
	if subtle.ConstantTimeCompare([]byte(id.sign(parts[1])), []byte(parts[2])) != 1 {
		return payload{}, core.AuthInvalid
	}
	data, err := b64.DecodeString(parts[1])
	if err != nil {
		return payload{}, core.AuthInvalid
	}
	var pl payload
	if json.Unmarshal(data, &pl) != nil {
		return payload{}, core.AuthInvalid
	}
	switch pl.K {
	case "n":
		if !id.opts.NameEnabled || !id.nameAllowed(pl.N) {
			return payload{}, core.AuthInvalid
		}
	case "c":
		u := id.user(pl.U)
		if u == nil {
			return payload{}, core.AuthInvalid
		}
		pl.N = u.Name
	default:
		return payload{}, core.AuthInvalid
	}
	if pl.E*1000 <= id.now() {
		return payload{}, core.AuthExpired
	}
	return pl, core.AuthOK
}

func (id *Identity) user(userID string) *ConfiguredUser {
	for i := range id.opts.Users {
		if id.opts.Users[i].ID == userID {
			return &id.opts.Users[i]
		}
	}
	return nil
}

// ------------------------------------------------------------ auth.name

func (id *Identity) nameAllowed(name string) bool {
	return len(id.opts.AllowedNames) == 0 || slices.Contains(id.opts.AllowedNames, name)
}

// NameUserID is the stable user id of a display-name login: n- plus 20 hex digits of the name's SHA-256.
func NameUserID(name string) string {
	h := sha256.Sum256([]byte(name))
	return "n-" + hex.EncodeToString(h[:])[:20]
}

func (id *Identity) LoginWithName(name string, _ core.LoginContext) (core.TokenGrant, error) {
	clean := p.TruncateUTF8(strings.TrimSpace(name), 64)
	if !id.nameAllowed(clean) {
		return core.TokenGrant{}, p.NewError(p.NotAllowed, "this name may not log in here")
	}
	for _, u := range id.opts.Users {
		if u.Name == clean {
			if u.Password != "" {
				return core.TokenGrant{}, p.NewError(p.NotAllowed, "this name is taken; log in with a code")
			}
			return id.issue(core.User{UserID: u.ID, Name: u.Name}, "n", false), nil
		}
	}
	return id.issue(core.User{UserID: NameUserID(clean), Name: clean}, "n", false), nil
}

// ------------------------------------------------------------ auth.code

func (id *Identity) limit(key string, perMinute int) error {
	now := id.now()
	var recent []int64
	for _, t := range id.attempts[key] {
		if now-t < 60_000 {
			recent = append(recent, t)
		}
	}
	if len(recent) >= perMinute {
		id.attempts[key] = recent
		return p.NewError(p.Throttled, "too many attempts", "retryAfterMs", max(1, 60_000-(now-recent[0])))
	}
	id.attempts[key] = append(recent, now)
	return nil
}

func (id *Identity) cleanup() {
	now := id.now()
	for code, pd := range id.pending {
		if now > pd.expiresAt+600_000 {
			delete(id.pending, code)
		}
	}
	for key, list := range id.attempts {
		if len(list) == 0 || now-list[len(list)-1] >= 60_000 {
			delete(id.attempts, key)
		}
	}
}

func (id *Identity) StartCodeLogin(ctx core.LoginContext) (core.CodeLoginStart, error) {
	id.mu.Lock()
	defer id.mu.Unlock()
	id.cleanup()
	if err := id.limit("start:"+ctx.RemoteAddress, 20); err != nil {
		return core.CodeLoginStart{}, err
	}
	code := core.NewCode()
	for id.pending[code] != nil {
		code = core.NewCode()
	}
	ttl := id.opts.CodeTTLSeconds
	id.pending[code] = &pending{expiresAt: id.now() + ttl*1000, state: "waiting", agent: ctx.Agent}
	shown := code[:4] + "-" + code[4:]
	return core.CodeLoginStart{
		LoginCode:        code,
		URL:              p.TruncateUTF8(ctx.BaseURL+"auth/confirm?code="+shown, 1000),
		ExpiresInSeconds: ttl,
		IntervalSeconds:  id.opts.CodeIntervalSecs,
	}, nil
}

func (id *Identity) PollCodeLogin(code string) (core.CodeLoginState, error) {
	id.mu.Lock()
	defer id.mu.Unlock()
	pd := id.pending[code]
	switch {
	case pd == nil:
		return core.CodeLoginState{State: "expired"}, nil
	case pd.state == "confirmed":
		return core.CodeLoginState{State: "confirmed", Grant: pd.grant}, nil
	case pd.state == "denied":
		return core.CodeLoginState{State: "denied"}, nil
	case id.now() >= pd.expiresAt:
		return core.CodeLoginState{State: "expired"}, nil
	}
	return core.CodeLoginState{State: "waiting"}, nil
}

func equal(a, b string) bool {
	ha, hb := sha256.Sum256([]byte(a)), sha256.Sum256([]byte(b))
	return subtle.ConstantTimeCompare(ha[:], hb[:]) == 1
}

// Confirm confirms or denies a pending login code: "ok", "unknown_code" or "bad_credentials".
func (id *Identity) Confirm(code, user, password string, deny bool) string {
	id.mu.Lock()
	defer id.mu.Unlock()
	pd := id.pending[core.NormalizeCode(code)]
	if pd == nil || pd.state != "waiting" || id.now() >= pd.expiresAt {
		return "unknown_code"
	}
	var u *ConfiguredUser
	for i := range id.opts.Users {
		if id.opts.Users[i].ID == user || id.opts.Users[i].Name == user {
			u = &id.opts.Users[i]
		}
	}
	if u == nil || u.Password == "" || !equal(u.Password, password) {
		return "bad_credentials"
	}
	if deny {
		pd.state = "denied"
		return "ok"
	}
	pd.grant = id.issue(core.User{UserID: u.ID, Name: u.Name}, "c", pd.agent)
	pd.state = "confirmed"
	return "ok"
}

// HandleHTTP serves the confirmation page below the base address (auth/confirm).
func (id *Identity) HandleHTTP(w http.ResponseWriter, r *http.Request, subpath string, body []byte, remote string) bool {
	if subpath != "auth/confirm" || !id.opts.CodeEnabled {
		return false
	}
	message := ""
	code := r.URL.Query().Get("code")
	agent := false
	if r.Method == http.MethodPost {
		form, _ := url.ParseQuery(string(body))
		code = form.Get("code")
		id.mu.Lock()
		if pd := id.pending[core.NormalizeCode(code)]; pd != nil {
			agent = pd.agent
		}
		err := id.limit("confirm:"+remote, 10)
		id.mu.Unlock()
		if err != nil {
			message = "Too many attempts — wait a minute."
		} else {
			deny := form.Get("action") == "deny"
			switch id.Confirm(code, form.Get("user"), form.Get("password"), deny) {
			case "ok":
				if deny {
					message = "Login denied. You can close this page."
				} else {
					message = "Login confirmed. Return to dzStudio."
				}
			case "unknown_code":
				message = "This code is unknown or has expired."
			default:
				message = "User name or password is wrong."
			}
		}
	}
	var b strings.Builder
	b.WriteString(`<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>dzStudio login</title><style>body{font:16px system-ui,sans-serif;max-width:28rem;margin:3rem auto;padding:0 1rem}
label{display:block;margin:.8rem 0 .2rem}input{width:100%;padding:.5rem;font:inherit;box-sizing:border-box}
button{margin:1rem .5rem 0 0;padding:.5rem 1rem;font:inherit}.msg{padding:.6rem;background:#eef}</style></head><body>
<h1>Confirm dzStudio login</h1>`)
	if r.Method != http.MethodPost {
		id.mu.Lock()
		if pd := id.pending[core.NormalizeCode(code)]; pd != nil {
			agent = pd.agent
		}
		id.mu.Unlock()
	}
	if agent {
		b.WriteString(`<p class="msg">An AI agent is asking to log in with your account. It will work in sessions as your agent; it can never save, manage or invite.</p>`)
	}
	if message != "" {
		b.WriteString(`<p class="msg">` + html.EscapeString(message) + `</p>`)
	}
	b.WriteString(`<form method="post"><label>Login code</label><input name="code" value="` + html.EscapeString(code) + `" autocomplete="off">
<label>User</label><input name="user" autocomplete="username"><label>Password</label><input name="password" type="password" autocomplete="current-password">
<button name="action" value="confirm">Confirm</button><button name="action" value="deny">Deny</button></form></body></html>`)
	w.Header().Set("content-type", "text/html; charset=utf-8")
	w.Header().Set("cache-control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(b.String()))
	return true
}
