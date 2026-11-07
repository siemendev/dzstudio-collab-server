package core

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	p "github.com/siemendev/dzstudio-collab-server/protocol"
)

const (
	PartBytes          = p.MB
	MaxOpBytes         = 3_670_016
	MaxChainOps        = 16
	minRequestBytes    = 4_194_304
	ResponseBytes      = 4_194_304
	liveIntervalMs     = 200
	liveMinGapMs       = 180
	liveCommitKeepMs   = 10_000
	lockLapseS         = 300
	idleS              = 300
	interruptedMs      = 10_000
	graceMs            = 30_000
	hostGoneMs         = 300_000
	creatorGoneMs      = 300_000
	participantTimeout = 600_000
	endedKeepMs        = 600_000
	requestKeepMs      = 600_000
	snapshotKeepMs     = 600_000
	inviteTTLMs        = 24 * 3600_000
	onlineMs           = 60_000
	maxLocksPerPart    = 20_000
	maxLocksPerSession = 25_000
	maxNotices         = 200
	redeemFailures     = 10
	maxHeadFiles       = 5000
	listPageSize       = 200
	versionReserve     = 10_000
)

type Limits struct {
	MaxRequestBytes  int64
	MaxWaitMs        int64
	MaxParticipants  int
	MaxSnapshotBytes int64
	CheckpointOps    int64
	CheckpointBytes  int64
	// Sessions one user may have started and not ended, by kind; 0 = no limit.
	MaxLocalSessionsPerUser  int
	MaxHostedSessionsPerUser int
}

var DefaultLimits = Limits{
	MaxRequestBytes:  8_388_608,
	MaxWaitMs:        8000,
	MaxParticipants:  8,
	MaxSnapshotBytes: 64 * p.MB,
	CheckpointOps:    1000,
	CheckpointBytes:  8 * p.MB,
}

type Capabilities struct {
	Users      bool
	InviteUser bool
	Hosted     bool
	Agents     bool
}

type Options struct {
	ServerName   string
	Software     string
	Notice       string
	Capabilities Capabilities
	Limits       Limits
	// AllowedClients: if set, session/create and session/join refuse other client names with bad_request.
	AllowedClients []string
	ClientVersions ClientVersions
}

type Deps struct {
	Identity      IdentityProvider
	NameLogin     NameLogin // nil: no auth.name
	CodeLogin     CodeLogin // nil: no auth.code
	Permissions   PermissionPolicy
	Directory     UserDirectory // nil: no users
	Storage       SessionStorage
	Attachments   AttachmentStorage // nil: in memory, lost on a restart
	ProjectStores []ProjectStore
	Clock         Clock
	Logger        Logger
	Hooks         SessionHooks // nil: none
}

type RequestContext struct {
	RemoteAddress string
	BaseURL       string

	agentToken bool
}

type cachedRequest struct {
	at     int64
	answer *p.Obj
	done   chan struct{}
	err    *p.Error
}

type layout struct {
	bytes int64
	parts int
}

// Service is the protocol core.
type Service struct {
	opts         Options
	deps         Deps
	limits       Limits
	clientPolicy atomic.Pointer[clientPolicy]

	mu            sync.Mutex
	sessions      map[string]*session
	invites       map[string]*invite
	requests      map[string]*cachedRequest
	requestsSaved int
	redeemFails   map[string][]int64
	userSeen      map[string]userSeen
	hostedLocks   map[string]*sync.Mutex
	layouts       map[string]layout

	stop     chan struct{}
	stopped  bool
	wg       sync.WaitGroup
	handlers map[string]handler
}

type userSeen struct {
	user User
	at   int64
}

type handler func(body *p.R, ctx RequestContext, u *User) *p.Obj

var requestIDEndpoints = map[string]bool{
	"auth/name": true, "session/create": true, "session/join": true, "invite/create": true,
	"invite/redeem": true, "project/save": true, "locks/release": true,
}

var noTokenEndpoints = map[string]bool{"info": true, "auth/name": true, "auth/code/start": true, "auth/code/poll": true}

// agentNever are the requests an agent may never make; with a token bound to an agent they fail before existence.
var agentNever = map[string]bool{
	"project/save": true, "session/end": true, "participant/remove": true, "locks/release": true, "invite/create": true,
}

func New(opts Options, deps Deps) (*Service, error) {
	l := opts.Limits
	d := DefaultLimits
	if l.MaxRequestBytes == 0 {
		l.MaxRequestBytes = d.MaxRequestBytes
	}
	if l.MaxRequestBytes < minRequestBytes {
		l.MaxRequestBytes = minRequestBytes
	}
	if l.MaxWaitMs < 8000 {
		l.MaxWaitMs = 8000
	}
	if l.MaxParticipants == 0 {
		l.MaxParticipants = d.MaxParticipants
	}
	if l.MaxParticipants < 2 {
		l.MaxParticipants = 2
	}
	if l.MaxSnapshotBytes == 0 {
		l.MaxSnapshotBytes = d.MaxSnapshotBytes
	}
	if l.CheckpointOps == 0 {
		l.CheckpointOps = d.CheckpointOps
	}
	if l.CheckpointBytes == 0 {
		l.CheckpointBytes = d.CheckpointBytes
	}
	if deps.Attachments == nil {
		deps.Attachments = newMemAttachments()
	}
	if opts.Capabilities.Hosted && len(deps.ProjectStores) == 0 {
		return nil, errors.New("capability hosted needs at least one project store")
	}
	if deps.Identity == nil || deps.Permissions == nil || deps.Storage == nil || deps.Clock == nil {
		return nil, errors.New("identity, permissions, storage and clock are required")
	}
	policy, err := compileClientVersions(opts.ClientVersions)
	if err != nil {
		return nil, err
	}
	s := &Service{
		opts:        opts,
		deps:        deps,
		limits:      l,
		sessions:    map[string]*session{},
		invites:     map[string]*invite{},
		requests:    map[string]*cachedRequest{},
		redeemFails: map[string][]int64{},
		userSeen:    map[string]userSeen{},
		hostedLocks: map[string]*sync.Mutex{},
		layouts:     map[string]layout{},
		stop:        make(chan struct{}),
	}
	s.clientPolicy.Store(policy)
	s.handlers = map[string]handler{
		"info":               s.hInfo,
		"auth/name":          s.hAuthName,
		"auth/code/start":    s.hAuthCodeStart,
		"auth/code/poll":     s.hAuthCodePoll,
		"sessions/list":      s.hSessionsList,
		"users/list":         s.hUsersList,
		"session/create":     s.hSessionCreate,
		"snapshot/put":       s.hSnapshotPut,
		"snapshot/get":       s.hSnapshotGet,
		"session/join":       s.hSessionJoin,
		"ops/send":           s.hOpsSend,
		"poll":               s.hPoll,
		"locks/set":          s.hLocksSet,
		"locks/release":      s.hLocksRelease,
		"live/put":           s.hLivePut,
		"invite/create":      s.hInviteCreate,
		"invite/redeem":      s.hInviteRedeem,
		"participant/remove": s.hParticipantRemove,
		"project/save":       s.hProjectSave,
		"project/list":       s.hProjectList,
		"project/revision":   s.hProjectRevision,
		"project/load":       s.hProjectLoad,
		"attachment/put":     s.hAttachmentPut,
		"attachment/get":     s.hAttachmentGet,
		"attachment/want":    s.hAttachmentWant,
		"session/leave":      s.hSessionLeave,
		"session/end":        s.hSessionEnd,
	}
	return s, nil
}

func (s *Service) Limits() Limits { return s.limits }

func (s *Service) now() int64 { return s.deps.Clock.Now() }

var logLineBreaks = strings.NewReplacer("\r", `\r`, "\n", `\n`)

func (s *Service) logInfo(format string, args ...any) {
	if s.deps.Logger != nil {
		s.deps.Logger.Info(logLineBreaks.Replace(fmt.Sprintf(format, args...)))
	}
}

func (s *Service) logError(msg string, err error) {
	if s.deps.Logger != nil {
		s.deps.Logger.Error(msg, err)
	}
}

// ------------------------------------------------------------ lifecycle

// Start restores sessions and the retry window from storage and starts the time rules.
func (s *Service) Start() error {
	stored, err := s.deps.Storage.LoadAll()
	if err != nil {
		return err
	}
	now := s.now()
	s.mu.Lock()
	for _, st := range stored {
		ss, invs, err := restoreSession(st, now)
		if err != nil {
			s.logError("could not restore a session", err)
			continue
		}
		if ss.ended != nil && now-ss.ended.At > endedKeepMs {
			_ = s.deps.Storage.DeleteSession(ss.ID)
			continue
		}
		for _, inv := range invs {
			s.invites[inv.Code] = inv
		}
		s.sessions[ss.ID] = ss
	}
	reqs, err := s.deps.Storage.LoadRequests()
	if err != nil {
		s.mu.Unlock()
		return err
	}
	var keep []StoredRequest
	for _, r := range reqs {
		if now-r.At > requestKeepMs {
			continue
		}
		var m map[string]any
		dec := json.NewDecoder(strings.NewReader(r.Answer))
		dec.UseNumber()
		if dec.Decode(&m) != nil {
			continue
		}
		done := make(chan struct{})
		close(done)
		s.requests[r.Key] = &cachedRequest{at: r.At, answer: fromJSON(m), done: done}
		keep = append(keep, r)
	}
	s.requestsSaved = len(keep)
	err = s.deps.Storage.CompactRequests(keep)
	s.mu.Unlock()
	if err != nil {
		return err
	}
	s.wg.Add(1)
	go s.timers()
	return nil
}

func (s *Service) timers() {
	defer s.wg.Done()
	sweep := time.NewTicker(250 * time.Millisecond)
	defer sweep.Stop()
	for {
		select {
		case <-s.stop:
			return
		case <-sweep.C:
			s.Tick()
		}
	}
}

// Stop ends the timers, wakes every waiting poll and writes what is still unwritten.
func (s *Service) Stop() {
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return
	}
	s.stopped = true
	close(s.stop)
	for _, ss := range s.sessions {
		s.notify(ss)
	}
	s.flushRecords()
	s.mu.Unlock()
	s.wg.Wait()
	if ds, ok := s.deps.Storage.(DeferredStorage); ok {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := ds.WaitDurable(ctx, ds.Mark()); err != nil {
			s.logError("writes still queued at stop", err)
		}
	}
}

// Tick runs the time rules now (also every 250 ms).
func (s *Service) Tick() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		return
	}
	s.sweep()
	s.flushRecords()
}

// ------------------------------------------------------------ dispatch

// Dispatch answers one protocol request; it never fails, errors come back as error answers.
func (s *Service) Dispatch(endpoint string, raw map[string]any, ctx RequestContext) (answer *p.Obj) {
	h, ok := s.handlers[endpoint]
	if !ok {
		return p.NewError(p.BadRequest, "unknown endpoint "+endpoint).Body()
	}
	if endpoint == "info" {
		return s.info()
	}
	deadline := s.deadlineFor(endpoint, raw)
	run := func() *p.Obj {
		res := s.guard(endpoint, func() *p.Obj { return s.dispatchInner(endpoint, h, raw, ctx) })
		if ds, ok := s.deps.Storage.(DeferredStorage); ok {
			wctx, cancel := context.WithTimeout(context.Background(), deadline)
			defer cancel()
			if err := ds.WaitDurable(wctx, ds.Mark()); err != nil {
				s.logError("an answer waited for the log in vain", err)
				return p.NewError(p.Busy, "the session log is not reachable right now", "retryAfterMs", 2000).Body()
			}
		}
		return res
	}
	ch := make(chan *p.Obj, 1)
	go func() { ch <- run() }()
	select {
	case res := <-ch:
		return res
	case <-time.After(deadline):
		return p.NewError(p.Busy, "server busy", "retryAfterMs", 1000).Body()
	}
}

// guard turns a protocol error raised with p.Fail into its answer, and anything else into internal.
func (s *Service) guard(endpoint string, f func() *p.Obj) (res *p.Obj) {
	defer func() {
		if r := recover(); r != nil {
			if pe, ok := r.(*p.Error); ok {
				res = pe.Body()
				return
			}
			s.logError(fmt.Sprintf("internal error in %s: %v\n%s", endpoint, r, debug.Stack()), nil)
			res = p.NewError(p.Internal, "internal error").Body()
		}
	}()
	return f()
}

func (s *Service) dispatchInner(endpoint string, h handler, raw map[string]any, ctx RequestContext) *p.Obj {
	r := p.NewR(raw)
	p.ParseProtocol(raw["protocol"])
	s.checkClientVersion(endpoint, raw)
	s.checkCapability(endpoint)
	var user *User
	if !noTokenEndpoints[endpoint] {
		u := s.authenticate(raw["token"])
		user = &u
		ctx.agentToken = s.isAgentToken(raw["token"])
		if ctx.agentToken && agentNever[endpoint] {
			p.Fail(p.NotAllowed, "an agent may not do this")
		}
	}
	var part *participant
	if user != nil {
		s.mu.Lock()
		s.userSeen[user.UserID] = userSeen{*user, s.now()}
		part = s.contactStart(raw, *user)
		s.mu.Unlock()
	}
	defer func() {
		if part != nil {
			s.mu.Lock()
			s.contactEnd(part)
			s.mu.Unlock()
		}
	}()
	if requestIDEndpoints[endpoint] {
		requestID := r.RequestID()
		who := ""
		if user != nil {
			who = user.UserID
		} else {
			name, _ := raw["name"].(string)
			who = "name:" + name
		}
		return s.idempotent(who+"|"+endpoint+"|"+requestID, func() *p.Obj { return h(r, ctx, user) })
	}
	return h(r, ctx, user)
}

func (s *Service) checkCapability(endpoint string) {
	c := s.opts.Capabilities
	switch {
	case endpoint == "users/list" && (!c.Users || s.deps.Directory == nil):
		p.Fail(p.CapabilityMissing, "users")
	case strings.HasPrefix(endpoint, "project/") && !c.Hosted:
		p.Fail(p.CapabilityMissing, "hosted")
	case endpoint == "auth/name" && s.deps.NameLogin == nil:
		p.Fail(p.CapabilityMissing, "auth.name")
	case strings.HasPrefix(endpoint, "auth/code/") && s.deps.CodeLogin == nil:
		p.Fail(p.CapabilityMissing, "auth.code")
	}
}

func (s *Service) authenticate(token any) User {
	t, isString := token.(string)
	if token == nil || (isString && t == "") {
		p.Fail(p.AuthRequired, "")
	}
	if !isString {
		p.Fail(p.TokenInvalid, "")
	}
	u, st := s.deps.Identity.Authenticate(t)
	switch st {
	case AuthExpired:
		p.Fail(p.TokenExpired, "")
	case AuthInvalid:
		p.Fail(p.TokenInvalid, "")
	}
	if !p.IsID(u.UserID) {
		p.Fail(p.Internal, "identity provider returned an invalid userId")
	}
	return User{UserID: u.UserID, Name: p.TruncateUTF8(u.Name, 64)}
}

func (s *Service) isAgentToken(token any) bool {
	at, ok := s.deps.Identity.(AgentTokens)
	t, _ := token.(string)
	return ok && s.opts.Capabilities.Agents && at.IsAgentToken(t)
}

func (s *Service) deadlineFor(endpoint string, raw map[string]any) time.Duration {
	switch endpoint {
	case "poll":
		w := int64(5000)
		if n, ok := raw["waitMs"].(json.Number); ok {
			if f, err := n.Float64(); err == nil {
				w = int64(f)
			}
		}
		w = min(max(w, 0), s.limits.MaxWaitMs)
		return time.Duration(w+900) * time.Millisecond
	case "project/save":
		return 8500 * time.Millisecond
	}
	return 4500 * time.Millisecond
}

// idempotent runs a requestId request once; repetitions get the stored answer, also after a restart.
func (s *Service) idempotent(key string, run func() *p.Obj) *p.Obj {
	s.mu.Lock()
	if c, ok := s.requests[key]; ok {
		s.mu.Unlock()
		<-c.done
		if c.answer != nil {
			return c.answer
		}
		panic(c.err)
	}
	c := &cachedRequest{at: s.now(), done: make(chan struct{})}
	s.requests[key] = c
	s.mu.Unlock()

	var answer *p.Obj
	func() {
		defer func() {
			if r := recover(); r != nil {
				s.mu.Lock()
				delete(s.requests, key)
				s.mu.Unlock()
				if pe, ok := r.(*p.Error); ok {
					c.err = pe
				} else {
					c.err = p.NewError(p.Internal, "internal error")
				}
				close(c.done)
				panic(r)
			}
		}()
		answer = run()
	}()
	if ok, _ := answer.Get("ok"); ok != true {
		s.mu.Lock()
		delete(s.requests, key)
		s.mu.Unlock()
		c.answer = answer
		close(c.done)
		return answer
	}
	s.mu.Lock()
	c.at = s.now()
	s.flushRecords()
	err := s.deps.Storage.AppendRequest(StoredRequest{Key: key, At: c.at, Answer: p.Serialize(answer)})
	s.requestsSaved++
	c.answer = answer
	s.mu.Unlock()
	if err != nil {
		s.logError("could not store an answer for the retry window", err)
	}
	close(c.done)
	return answer
}

func (s *Service) contactStart(raw map[string]any, u User) *participant {
	sid, _ := raw["sessionId"].(string)
	pid, _ := raw["participantId"].(string)
	if sid == "" || pid == "" {
		return nil
	}
	ss := s.sessions[sid]
	if ss == nil {
		return nil
	}
	pt := ss.participants[pid]
	if pt == nil || pt.User.UserID != u.UserID || pt.Status != "active" {
		return nil
	}
	pt.openRequests++
	ss.lastActive = s.now()
	return pt
}

func (s *Service) contactEnd(pt *participant) {
	if pt.openRequests > 0 {
		pt.openRequests--
	}
	pt.lastContactEnd = s.now()
	pt.disconnectDone = false
}

func (s *Service) contactAge(pt *participant) int64 {
	if pt.openRequests > 0 {
		return 0
	}
	return max(0, s.now()-pt.lastContactEnd)
}

func (s *Service) idleNow(pt *participant) float64 {
	return pt.idleReported + float64(max(0, s.now()-pt.idleReportedAt))/1000
}

// ------------------------------------------------------------ info and auth

func (s *Service) capabilityList() []string {
	caps := []string{}
	c := s.opts.Capabilities
	if c.Users && s.deps.Directory != nil {
		caps = append(caps, "users")
	}
	if c.InviteUser {
		caps = append(caps, "invite.user")
	}
	if c.Hosted {
		caps = append(caps, "hosted")
	}
	if c.Agents {
		caps = append(caps, "agents")
	}
	if s.deps.CodeLogin != nil {
		caps = append(caps, "auth.code")
	}
	if s.deps.NameLogin != nil {
		caps = append(caps, "auth.name")
	}
	return caps
}

func (s *Service) info() *p.Obj {
	server := p.O("name", p.TruncateUTF8(s.opts.ServerName, 64))
	if s.opts.Software != "" {
		server.Set("software", s.opts.Software)
	}
	auth := []*p.Obj{}
	for _, m := range s.deps.Identity.AuthMethods() {
		o := p.O("method", m.Method)
		if m.Method == "provider" {
			o.Set("provider", m.Provider)
		}
		auth = append(auth, o)
	}
	out := p.O(
		"ok", true,
		"server", server,
		"protocols", []*p.Obj{p.O("major", 1, "minor", 0)},
		"capabilities", s.capabilityList(),
		"auth", auth,
		"limits", p.O(
			"maxRequestBytes", s.limits.MaxRequestBytes,
			"maxWaitMs", s.limits.MaxWaitMs,
			"maxParticipants", s.limits.MaxParticipants,
			"maxOpBytes", MaxOpBytes,
			"maxChainOps", MaxChainOps,
			"liveIntervalMs", liveIntervalMs,
			"lockLapseSeconds", lockLapseS,
			"connectionGraceSeconds", graceMs/1000,
			"hostGoneSeconds", hostGoneMs/1000,
		),
	)
	if s.opts.Notice != "" {
		out.Set("notice", s.opts.Notice)
	}
	s.setClientVersionInfo(out)
	return out
}

func (s *Service) hInfo(_ *p.R, _ RequestContext, _ *User) *p.Obj { return s.info() }

func grantObj(g TokenGrant) *p.Obj {
	o := p.O("ok", true, "token", g.Token, "name", g.Name)
	if g.TokenExpiresInSeconds > 0 {
		o.Set("tokenExpiresInSeconds", g.TokenExpiresInSeconds)
	}
	return o
}

func must[T any](v T, err error) T {
	if err != nil {
		var pe *p.Error
		if errors.As(err, &pe) {
			panic(pe)
		}
		panic(err)
	}
	return v
}

func (s *Service) hAuthName(r *p.R, ctx RequestContext, _ *User) *p.Obj {
	name := r.String("name", 64, 1)
	if strings.TrimSpace(name) == "" {
		p.BadRequestf("name must not be blank")
	}
	return grantObj(must(s.deps.NameLogin.LoginWithName(name, LoginContext{RemoteAddress: ctx.RemoteAddress, BaseURL: ctx.BaseURL})))
}

func (s *Service) hAuthCodeStart(r *p.R, ctx RequestContext, _ *User) *p.Obj {
	agent, agentName := false, ""
	if c := r.OptObject("client"); c != nil && s.clientKind(c) == "agent" {
		agent, agentName = true, s.readAgentName(c)
	}
	st := must(s.deps.CodeLogin.StartCodeLogin(LoginContext{RemoteAddress: ctx.RemoteAddress, BaseURL: ctx.BaseURL,
		Agent: agent, AgentName: agentName}))
	return p.O("ok", true, "loginCode", st.LoginCode, "url", st.URL, "expiresInSeconds", st.ExpiresInSeconds,
		"intervalSeconds", max(2, st.IntervalSeconds))
}

func (s *Service) hAuthCodePoll(r *p.R, _ RequestContext, _ *User) *p.Obj {
	code := r.String("loginCode", 64, 0)
	st := must(s.deps.CodeLogin.PollCodeLogin(NormalizeCode(code)))
	if st.State == "confirmed" {
		o := grantObj(st.Grant)
		o.Set("state", "confirmed")
		return o
	}
	return p.O("ok", true, "state", st.State)
}

// ------------------------------------------------------------ codes and ids

const CodeAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"

// NewCode gives 8 characters from a 32-letter alphabet: 40 bits from the system's secure random source.
func NewCode() string {
	b := make([]byte, 8)
	for i := range b {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(CodeAlphabet))))
		if err != nil {
			panic(err)
		}
		b[i] = CodeAlphabet[n.Int64()]
	}
	return string(b)
}

// NormalizeCode drops case, spaces and hyphens of a typed code.
func NormalizeCode(in string) string {
	var b strings.Builder
	for _, r := range in {
		if r == '-' || r == ' ' || r == '\t' || r == '\n' || r == '\r' {
			continue
		}
		b.WriteRune(r)
	}
	return strings.ToUpper(b.String())
}

const idAlphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"

func newID(prefix string) string {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	out := []byte(prefix)
	for _, c := range b {
		out = append(out, idAlphabet[int(c)%len(idAlphabet)])
	}
	return string(out)
}

// fromJSON turns a decoded JSON object back into an ordered object (for stored answers).
func fromJSON(m map[string]any) *p.Obj {
	o := p.NewObj()
	for k, v := range m {
		o.Set(k, fromJSONValue(v))
	}
	return o
}

func fromJSONValue(v any) any {
	switch x := v.(type) {
	case map[string]any:
		return fromJSON(x)
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = fromJSONValue(e)
		}
		return out
	case json.Number:
		return p.Raw(string(x))
	}
	return v
}
