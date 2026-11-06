// Package reference wires the default self-hosting adapters to the core and the HTTP layer.
package reference

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/siemendev/dzstudio-collab-server/adapters/configperms"
	"github.com/siemendev/dzstudio-collab-server/adapters/filestore"
	"github.com/siemendev/dzstudio-collab-server/adapters/fsprojects"
	"github.com/siemendev/dzstudio-collab-server/adapters/localidentity"
	"github.com/siemendev/dzstudio-collab-server/adapters/memstore"
	"github.com/siemendev/dzstudio-collab-server/config"
	"github.com/siemendev/dzstudio-collab-server/core"
	"github.com/siemendev/dzstudio-collab-server/httpapi"
	p "github.com/siemendev/dzstudio-collab-server/protocol"
)

const Software = "dzstudio-collab-server 0.2.0 (Go)"

// Clock is system time plus an offset that the test controls move forward. With testing enabled the offset is kept
// in the data directory so that it survives restarts.
type Clock struct {
	mu     sync.Mutex
	offset int64
	file   string
}

func (c *Clock) Now() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return time.Now().UnixMilli() + c.offset
}

func (c *Clock) Advance(ms int64) int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.offset += ms
	if c.file != "" {
		_ = os.WriteFile(c.file, []byte(strconv.FormatInt(c.offset, 10)), 0o644)
	}
	return c.offset
}

type Server struct {
	Service  *core.Service
	Handler  http.Handler
	Identity *localidentity.Identity
	Stores   []*fsprojects.Store
	Clock    *Clock
}

type logger struct{ log func(string) }

func (l logger) Info(msg string) { l.log(msg) }
func (l logger) Error(msg string, err error) {
	if err != nil {
		msg += ": " + err.Error()
	}
	l.log(msg)
}

func New(cfg *config.Config, log func(string)) (*Server, error) {
	clock := &Clock{}
	if cfg.Testing.Enabled {
		if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
			return nil, err
		}
		clock.file = filepath.Join(cfg.DataDir, "test-clock-offset")
		if data, err := os.ReadFile(clock.file); err == nil {
			clock.offset, _ = strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
		}
	}
	secret, err := cfg.TokenSecret()
	if err != nil {
		return nil, err
	}
	identity, err := localidentity.New(localidentity.Options{
		Users: cfg.Users, Secret: secret, TokenTTLSeconds: cfg.Auth.TokenTTLSeconds,
		NameEnabled: cfg.Auth.Name.Enabled, AllowedNames: cfg.Auth.Name.AllowedNames,
		CodeEnabled: cfg.Auth.Code.Enabled, CodeTTLSeconds: cfg.Auth.Code.ExpiresInSeconds, CodeIntervalSecs: cfg.Auth.Code.IntervalSeconds,
		Now: clock.Now,
	})
	if err != nil {
		return nil, err
	}
	var stores []*fsprojects.Store
	var ports []core.ProjectStore
	if cfg.Hosted.Enabled {
		for _, st := range cfg.Hosted.Stores {
			s := fsprojects.New(st.Provider, st.Root, st.Map, clock.Now)
			stores = append(stores, s)
			ports = append(ports, s)
		}
	}
	var storage core.SessionStorage
	var attachments core.AttachmentStorage
	if cfg.Storage.Type == "memory" {
		storage = memstore.New()
	} else {
		storage = filestore.New(filepath.Join(cfg.DataDir, "sessions"), cfg.Storage.Fsync)
		attachments = filestore.NewAttachments(filepath.Join(cfg.DataDir, "sessions"), cfg.Storage.Fsync)
	}
	var directory core.UserDirectory
	if cfg.Directory.Mode != "none" {
		directory = configperms.NewDirectory(cfg.Directory, func() []core.User {
			users := map[string]core.User{}
			for _, u := range identity.ConfiguredUsers() {
				users[u.UserID] = u
			}
			for _, u := range identity.Seen(clock.Now() - 24*3600_000) {
				users[u.UserID] = u
			}
			out := make([]core.User, 0, len(users))
			for _, u := range users {
				out = append(out, u)
			}
			return out
		})
	}
	deps := core.Deps{
		Identity: identity, Permissions: configperms.New(cfg.Permissions), Directory: directory,
		Storage: storage, Attachments: attachments, ProjectStores: ports, Clock: clock, Logger: logger{log},
	}
	if cfg.Auth.Name.Enabled {
		deps.NameLogin = identity
	}
	if cfg.Auth.Code.Enabled {
		deps.CodeLogin = identity
	}
	service, err := core.New(core.Options{
		ServerName: cfg.Server.Name, Software: Software, Notice: cfg.Server.Notice,
		Capabilities: core.Capabilities{Users: directory != nil, InviteUser: cfg.InviteUser, Hosted: cfg.Hosted.Enabled, Agents: cfg.Agents.Enabled},
		Limits: core.Limits{
			MaxRequestBytes: cfg.Limits.MaxRequestBytes, MaxParticipants: cfg.Limits.MaxParticipants,
			MaxSnapshotBytes: cfg.Limits.MaxSnapshotBytes, CheckpointOps: cfg.Limits.CheckpointOps, CheckpointBytes: cfg.Limits.CheckpointBytes,
			MaxLocalSessionsPerUser: cfg.Limits.MaxLocalPerUser, MaxHostedSessionsPerUser: cfg.Limits.MaxHostedPerUser,
		},
		AllowedClients: cfg.AllowedClients,
		ClientVersions: core.ClientVersions{Min: cfg.ClientVersion.Min, Recommended: cfg.ClientVersion.Recommended, UpdateURL: cfg.ClientVersion.UpdateURL},
	}, deps)
	if err != nil {
		return nil, err
	}
	exts := []httpapi.Extension{identity}
	srv := &Server{Service: service, Identity: identity, Stores: stores, Clock: clock}
	var fault *netFault
	if cfg.Testing.Enabled {
		fault = &netFault{now: clock.Now, verify: identity.Verify, limit: service.Limits().MaxRequestBytes, base: httpapi.NormalizeBasePath(cfg.Listen.BasePath)}
		exts = append(exts, fault, testControls{srv})
	}
	srv.Handler = httpapi.Handler(httpapi.Options{
		Service: service, BasePath: cfg.Listen.BasePath, PublicURL: cfg.PublicURL, TrustProxy: cfg.TrustProxy, Extensions: exts,
	})
	if fault != nil {
		srv.Handler = fault.wrap(srv.Handler)
	}
	return srv, nil
}

// testControls: only with testing.enabled. They let a test suite move the clock, end sessions and reach into the
// project store over HTTP. Never on a server people use.
type testControls struct{ s *Server }

func (t testControls) store(provider string) *fsprojects.Store {
	for _, st := range t.s.Stores {
		if st.Provider() == provider {
			return st
		}
	}
	return nil
}

func (t testControls) HandleHTTP(w http.ResponseWriter, r *http.Request, sub string, body []byte, _ string) bool {
	if !strings.HasPrefix(sub, "_test/") {
		return false
	}
	var b struct {
		AdvanceMs   int64  `json:"advanceMs"`
		SessionID   string `json:"sessionId"`
		Checksum    string `json:"checksum"`
		Provider    string `json:"provider"`
		Key         string `json:"key"`
		Name        string `json:"name"`
		Content     string `json:"content"`
		ProjectName string `json:"projectName"`
		Map         string `json:"map"`
		Min         string `json:"min"`
		Recommended string `json:"recommended"`
		UpdateURL   string `json:"updateUrl"`
		Files       []struct {
			Name    string `json:"name"`
			Content string `json:"content"`
		} `json:"files"`
	}
	if len(body) > 0 && json.Unmarshal(body, &b) != nil {
		httpapi.Send(w, p.NewError(p.BadRequest, "body is not JSON").Body())
		return true
	}
	fail := func(msg string) { httpapi.Send(w, p.NewError(p.BadRequest, msg).Body()) }
	switch sub {
	case "_test/clock":
		off := t.s.Clock.Advance(b.AdvanceMs)
		t.s.Service.Tick()
		httpapi.Send(w, p.O("ok", true, "offsetMs", off))
	case "_test/session/end":
		httpapi.Send(w, p.O("ok", t.s.Service.EndByOperator(b.SessionID)))
	case "_test/attachment/forget":
		httpapi.Send(w, p.O("ok", true, "forgotten", t.s.Service.ForgetAttachment(b.SessionID, b.Checksum)))
	case "_test/client-version":
		if err := t.s.Service.SetClientVersions(core.ClientVersions{Min: b.Min, Recommended: b.Recommended, UpdateURL: b.UpdateURL}); err != nil {
			fail(err.Error())
			return true
		}
		httpapi.Send(w, p.O("ok", true))
	case "_test/store/revision":
		st := t.store(b.Provider)
		if st == nil {
			fail("unknown store")
			return true
		}
		rev, ok, err := st.CurrentRevision(b.Key)
		if err != nil {
			fail(err.Error())
			return true
		}
		if !ok {
			httpapi.Send(w, p.O("ok", true, "revision", p.Raw("null")))
			return true
		}
		httpapi.Send(w, p.O("ok", true, "revision", rev))
	case "_test/store/read":
		st := t.store(b.Provider)
		if st == nil {
			fail("unknown store")
			return true
		}
		head, files, err := st.Load(b.Key)
		if errors.Is(err, core.ErrProjectUnknown) {
			httpapi.Send(w, p.O("ok", true, "project", p.Raw("null")))
			return true
		}
		if err != nil {
			fail(err.Error())
			return true
		}
		list := []*p.Obj{}
		for _, f := range head.Files {
			pieces := p.SplitPieces(files[f.Name])
			if pieces == nil {
				pieces = []string{}
			}
			list = append(list, p.O("name", f.Name, "pieces", pieces))
		}
		httpapi.Send(w, p.O("ok", true, "project", p.O("revision", head.Revision, "files", list)))
	case "_test/store/edit":
		st := t.store(b.Provider)
		if st == nil {
			fail("unknown store")
			return true
		}
		files := map[string]string{}
		if b.Name != "" {
			files[b.Name] = b.Content
		}
		for _, f := range b.Files {
			files[f.Name] = f.Content
		}
		if err := st.EditOutside(b.Key, files, b.ProjectName, b.Map); err != nil {
			fail(err.Error())
			return true
		}
		pieces := p.SplitPieces(b.Content)
		if pieces == nil {
			pieces = []string{}
		}
		httpapi.Send(w, p.O("ok", true, "pieces", pieces))
	default:
		fail("unknown test control")
	}
	return true
}
