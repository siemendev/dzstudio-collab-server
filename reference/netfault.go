package reference

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/siemendev/dzstudio-collab-server/core"
	"github.com/siemendev/dzstudio-collab-server/httpapi"
	p "github.com/siemendev/dzstudio-collab-server/protocol"
)

const maxNetDelayMs = 600_000

// netRule is what _test/net sets: a connection that answers late, loses answers after the server applied the
// request, or is not there at all. UserID empty means every request; UntilMs 0 means until reset.
type netRule struct {
	UserID        string `json:"userId"`
	DelayMs       int64  `json:"delayMs"`
	DropResponses bool   `json:"dropResponses"`
	Refuse        bool   `json:"refuse"`
	UntilMs       int64  `json:"untilMs"`
}

func (r netRule) empty() bool { return r.DelayMs == 0 && !r.DropResponses && !r.Refuse }

type netFault struct {
	mu     sync.Mutex
	rule   netRule
	now    func() int64
	verify func(token string) (core.User, core.AuthStatus)
	limit  int64
	base   string
}

func (f *netFault) set(body []byte) *p.Obj {
	var r netRule
	if len(bytes.TrimSpace(body)) > 0 {
		dec := json.NewDecoder(bytes.NewReader(body))
		dec.DisallowUnknownFields()
		if dec.Decode(&r) != nil {
			return p.NewError(p.BadRequest, "body must be {userId?, delayMs?, dropResponses?, refuse?, untilMs?}").Body()
		}
	}
	if r.DelayMs < 0 || r.DelayMs > maxNetDelayMs {
		return p.NewError(p.BadRequest, "delayMs must be 0 to 600000").Body()
	}
	if r.UntilMs < 0 {
		return p.NewError(p.BadRequest, "untilMs must be a time in ms since 1970 (server clock)").Body()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.empty() {
		r = netRule{}
	}
	f.rule = r
	return p.O("ok", true, "active", !r.empty(), "nowMs", f.now())
}

func (f *netFault) active() (netRule, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r := f.rule
	if r.empty() {
		return r, false
	}
	if r.UntilMs > 0 && f.now() >= r.UntilMs {
		f.rule = netRule{}
		return r, false
	}
	return r, true
}

func (f *netFault) matches(r netRule, body []byte) bool {
	if r.UserID == "" {
		return true
	}
	var b struct {
		Token string `json:"token"`
	}
	if json.Unmarshal(body, &b) != nil || b.Token == "" {
		return false
	}
	u, st := f.verify(b.Token)
	if st != core.AuthOK {
		return false
	}
	if name, ok := strings.CutPrefix(r.UserID, "name:"); ok {
		return u.Name == name
	}
	return u.UserID == r.UserID
}

func (f *netFault) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if strings.HasPrefix(req.URL.Path, f.base+"_test/") {
			next.ServeHTTP(w, req)
			return
		}
		rule, on := f.active()
		if !on {
			next.ServeHTTP(w, req)
			return
		}
		body, _ := io.ReadAll(io.LimitReader(req.Body, f.limit+1))
		req.Body = io.NopCloser(bytes.NewReader(body))
		if !f.matches(rule, body) {
			next.ServeHTTP(w, req)
			return
		}
		// ErrAbortHandler closes the connection without a byte of answer and without a log line.
		if rule.Refuse {
			panic(http.ErrAbortHandler)
		}
		rec := &recorder{header: http.Header{}, status: http.StatusOK}
		next.ServeHTTP(rec, req)
		if rule.DelayMs > 0 {
			t := time.NewTimer(time.Duration(rule.DelayMs) * time.Millisecond)
			select {
			case <-t.C:
			case <-req.Context().Done():
				t.Stop()
				return
			}
		}
		if rule.DropResponses {
			panic(http.ErrAbortHandler)
		}
		for k, v := range rec.header {
			w.Header()[k] = v
		}
		w.WriteHeader(rec.status)
		_, _ = w.Write(rec.buf.Bytes())
	})
}

func (f *netFault) HandleHTTP(w http.ResponseWriter, _ *http.Request, sub string, body []byte, _ string) bool {
	if sub != "_test/net" {
		return false
	}
	httpapi.Send(w, f.set(body))
	return true
}

type recorder struct {
	header http.Header
	status int
	buf    bytes.Buffer
}

func (r *recorder) Header() http.Header         { return r.header }
func (r *recorder) WriteHeader(status int)      { r.status = status }
func (r *recorder) Write(b []byte) (int, error) { return r.buf.Write(b) }
