// Package core holds every rule of dzcollab 1. It knows no operator: accounts, rights, the session log, hosted
// projects and the clock come in through the ports below.
package core

import (
	"context"
	"errors"
)

// User is a logged-in person. UserID is A–Z a–z 0–9 _ -, 1 to 64 characters; Name at most 64 bytes (the core cuts
// longer names at a character boundary).
type User struct {
	UserID string `json:"userId"`
	Name   string `json:"name"`
}

// ---------------------------------------------------------------- identity

type AuthMethod struct {
	Method   string // "provider", "code" or "name"
	Provider string // only for "provider"
}

type AuthStatus int

const (
	AuthOK AuthStatus = iota
	AuthInvalid
	AuthExpired
)

type TokenGrant struct {
	Token                 string
	Name                  string
	TokenExpiresInSeconds int64 // 0 = not said
}

type LoginContext struct {
	RemoteAddress string
	BaseURL       string // base address the client used, ends with /
	// Agent: auth/code/start named client.kind "agent" (capability agents); the token of this login should be bound
	// to an agent (AgentTokens), and the confirmation page should say that an agent is logging in.
	Agent bool
	// AgentName: client.agentName of such a login (one line, at most 64 bytes, may be empty); the confirmation page
	// may show it.
	AgentName string
}

// IdentityProvider turns tokens into users and names the login methods for info.
type IdentityProvider interface {
	AuthMethods() []AuthMethod
	Authenticate(token string) (User, AuthStatus)
}

// AgentTokens is optional for an IdentityProvider (capability agents): whether a token came from a login that named
// client.kind "agent". Every participant with such a token is an agent, whatever session/create or session/join names.
type AgentTokens interface {
	IsAgentToken(token string) bool
}

// NameLogin is auth/name (capability auth.name). Return a *protocol.Error with not_allowed to refuse a name.
type NameLogin interface {
	LoginWithName(name string, ctx LoginContext) (TokenGrant, error)
}

type CodeLoginStart struct {
	LoginCode        string
	URL              string
	ExpiresInSeconds int64
	IntervalSeconds  int64
}

type CodeLoginState struct {
	State string // waiting, confirmed, expired, denied
	Grant TokenGrant
}

// CodeLogin is auth/code/start and auth/code/poll (capability auth.code).
type CodeLogin interface {
	StartCodeLogin(ctx LoginContext) (CodeLoginStart, error)
	PollCodeLogin(loginCode string) (CodeLoginState, error)
}

// ---------------------------------------------------------------- permissions

type HostedRef struct {
	Provider string `json:"provider"`
	Key      string `json:"key"`
}

type ProjectContext struct {
	Kind        string // "local" or "hosted"
	Host        User   // local only
	HostedRef   HostedRef
	ProjectName string
	Map         string
}

// ProjectRights are the standing rights of a user on a project, independent of invitations.
type ProjectRights struct {
	Join   bool // see and join sessions without an invitation; also load hosted projects
	Save   bool // project/save (hosted only); also needed to start a hosted session
	Manage bool // remove participants, release locks, discard (hosted only; the host of a local session always manages)
	Invite bool // ask for invitations (only ever given to participants with standing rights)
}

type PermissionPolicy interface {
	MayHostLocal(u User) (bool, error)
	// Rights of u on a project. For local projects the host's own rights are never asked.
	Rights(u User, p ProjectContext) (ProjectRights, error)
}

// ProjectCreator is optional for a PermissionPolicy: whether u may create a new project, under a key of the
// client's choosing, in the store of provider (project/list › stores › canCreate). Without it, nobody is told so.
type ProjectCreator interface {
	MayCreate(u User, provider string) (bool, error)
}

// UserDirectory names the people a user sees as available (capability users), e.g. friends.
type UserDirectory interface {
	ListAvailable(u User) ([]User, error)
}

// ---------------------------------------------------------------- session log storage

type Chain struct {
	Index int `json:"index"`
	Count int `json:"count"`
}

// LogEntry is one appended operation (or one link of a chain).
type LogEntry struct {
	Seq           int64      `json:"seq"`
	ParticipantID string     `json:"participantId"`
	ClientSeq     int64      `json:"clientSeq"`
	OpID          string     `json:"opId"`
	System        bool       `json:"system,omitempty"`
	LiveID        string     `json:"liveId,omitempty"`
	Chain         *Chain     `json:"chain,omitempty"`
	Parts         [][]string `json:"parts"`
	// NS counts operations without system up to and including this one; a chain counts once, at its last link.
	NS int64 `json:"ns"`
}

// StagedItem is an accepted part or chain link that is not appended yet. It must survive a restart.
type StagedItem struct {
	ClientSeq int64    `json:"clientSeq"`
	OpID      string   `json:"opId"`
	System    bool     `json:"system,omitempty"`
	LiveID    string   `json:"liveId,omitempty"`
	Chain     *Chain   `json:"chain,omitempty"`
	Part      int      `json:"part"`
	Parts     int      `json:"parts"`
	Pieces    []string `json:"pieces"`
}

type SnapshotPart struct {
	Seq    int64    `json:"seq"`
	Part   int      `json:"part"`
	Parts  int      `json:"parts"`
	Pieces []string `json:"pieces"`
}

// StoredSession is what LoadAll gives back for one session.
type StoredSession struct {
	Record  []byte
	Entries []LogEntry
	Staged  map[string][]StagedItem // by participantId
	Values  map[string][]byte
}

// StoredRequest is one answer of the retry window (requestId endpoints).
type StoredRequest struct {
	Key    string `json:"key"`
	At     int64  `json:"at"`
	Answer string `json:"answer"` // serialized JSON object
}

// DeferredStorage is optional for a SessionStorage whose writes return as soon as they are queued, before they are
// durable (a broker that acknowledges later). The core calls such writes under its lock and then never answers a
// request before every write queued up to the moment its answer was built is durable: it takes Mark() once the
// answer is built and waits with WaitDurable outside its lock.
type DeferredStorage interface {
	Mark() uint64
	// WaitDurable returns nil once every write up to mark survives a restart. An error means a write was lost; every
	// later wait must fail as well (memory is ahead of the log — the process restarts and replays the log).
	WaitDurable(ctx context.Context, mark uint64) error
}

// SessionStorage holds the session log: snapshots, the operations after them, staged parts and links, the session
// record and the retry window. The core never overlaps calls for one session. Every call returns only once its data
// is durable for a restart.
type SessionStorage interface {
	LoadAll() ([]StoredSession, error)
	SaveRecord(sessionID string, record []byte) error
	Append(sessionID string, entries []LogEntry) error
	// Read gives entries with seq > after, in order, at most limit of them.
	Read(sessionID string, after int64, limit int) ([]LogEntry, error)
	// Truncate forgets entries with seq <= through.
	Truncate(sessionID string, through int64) error
	PutSnapshotPart(sessionID string, part SnapshotPart) error
	GetSnapshotPart(sessionID string, seq int64, part int) (*SnapshotPart, error)
	DropSnapshot(sessionID string, seq int64) error
	Stage(sessionID, participantID string, items []StagedItem) error
	Unstage(sessionID, participantID string) error
	PutValue(sessionID, key string, value []byte) error
	DeleteSession(sessionID string) error

	AppendRequest(r StoredRequest) error
	LoadRequests() ([]StoredRequest, error)
	// CompactRequests replaces the stored retry window with keep.
	CompactRequests(keep []StoredRequest) error
}

// ---------------------------------------------------------------- attachments

// AttachmentStorage keeps the parts of attachments (attachment/put), by session and the client's checksum (an opaque
// text of up to 200 bytes — encode it before using it as a file or object name). Pieces come back byte for byte with
// the same boundaries. Every call returns once its data survives a restart. Which attachments are complete is the
// core's bookkeeping (in the session record); the storage only holds parts.
type AttachmentStorage interface {
	PutPart(sessionID, checksum string, part int, pieces []string) error
	// GetPart gives the pieces of one part; ok false if the storage does not have it.
	GetPart(sessionID, checksum string, part int) (pieces []string, ok bool, err error)
	Drop(sessionID, checksum string) error
	DropSession(sessionID string) error
}

// ---------------------------------------------------------------- project store (hosted)

type ProjectFile struct {
	Name    string
	Content string
	// Checksum is the SHA-256 (64 lower-case hex digits) the client gave; the core has checked it already.
	Checksum string
}

type ProjectMeta struct {
	ProjectName string
	Map         string
}

// MergedChecksum is the checksum of one merged state (a target the store composes from several files).
type MergedChecksum struct {
	Target   string
	Checksum string
}

// SaveRequest is one project/save that passed the core's checks (fields, file checksums, not_confirmed,
// superseded and — without Force — ChangedSince).
type SaveRequest struct {
	Key     string
	Files   []ProjectFile
	Deleted []string
	// Merged are the client's expectations; nil if the request had none. Only a composing store looks at them.
	Merged []MergedChecksum
	Force  bool
	// BaseRevision is the revision of the session's last save (at first: hostedRevision of session/create);
	// CreatedRevision always the one of session/create.
	BaseRevision    string
	CreatedRevision string
	SessionID       string
	RequestID       string
	User            User
	Meta            ProjectMeta
}

type SaveResult struct {
	Revision string
	Files    []FileRevision
	// Merged: the store's checksums of its merged states after the write; nil for a store that composes nothing
	// (the core then names one entry per file with target = name).
	Merged []MergedChecksum
}

type FileRevision struct {
	Name     string
	Revision string
	// Checksum is the SHA-256 of the bytes the store wrote.
	Checksum string
}

// Mismatch is one entry of checksum_mismatch: Kind "file" or "merged"; Expected empty when the request named
// none, Actual empty when the store forms no state under this name.
type Mismatch struct {
	Kind     string
	Name     string
	Expected string
	Actual   string
}

// ChecksumMismatchError is what Save returns when a checksum differs; the store has written nothing.
type ChecksumMismatchError struct {
	Mismatches []Mismatch
}

func (e *ChecksumMismatchError) Error() string { return "checksum mismatch" }

type ProjectInfo struct {
	Key         string
	ProjectName string
	Map         string
	Revision    string
}

type FileInfo struct {
	Name     string
	Revision string // may be empty
	Bytes    int64
	// Hash identifies the content (any stable digest); the core caches the piece layout by it.
	Hash string
}

type ProjectHead struct {
	Revision    string
	ProjectName string
	Map         string
	Files       []FileInfo
}

var (
	// ErrRevisionUnknown: the revision is no longer (or never was) available.
	ErrRevisionUnknown = errors.New("revision unknown")
	// ErrFileUnknown: the revision has no such file.
	ErrFileUnknown = errors.New("file unknown")
	// ErrProjectUnknown: the store has nothing under this key.
	ErrProjectUnknown = errors.New("project unknown")
)

// ProjectStore holds the projects of hosted sessions (dzbot: mods and placements).
type ProjectStore interface {
	// Provider is the hostedRef provider this store answers for.
	Provider() string
	// CurrentRevision of the project; ok false if there is no such project yet.
	CurrentRevision(key string) (rev string, ok bool, err error)
	// ChangedSince names files changed (added, modified, removed) since revision; empty if unchanged. An unknown
	// revision counts as every file changed, except "" for a project that does not exist yet.
	ChangedSince(key, revision string) ([]string, error)
	// Save writes files and deletes deleted in one step — all or nothing. Other files stay untouched. Before the
	// write takes effect it forms the SHA-256 of every file over the bytes it stores and, if it composes merged
	// states, their checksums (compared with req.Merged unless req.Force); on a difference it writes nothing and
	// returns *ChecksumMismatchError. It may return a *protocol.Error (store_changed, not_allowed, busy).
	Save(req SaveRequest) (SaveResult, error)
	// List gives every project of the store, in a fixed order.
	List() ([]ProjectInfo, error)
	// Head describes one revision ("" = current) with its files. ErrProjectUnknown, ErrRevisionUnknown.
	Head(key, revision string) (*ProjectHead, error)
	// ReadFile gives the content of one file of a revision. Every answer for one revision describes the same state;
	// a revision replaced by a save stays readable for at least 10 minutes. ErrProjectUnknown, ErrRevisionUnknown,
	// ErrFileUnknown.
	ReadFile(key, revision, name string) (string, error)
}

// UserScopedStore is optional for a ProjectStore that checks rights itself (dzbot): the core then reads heads and
// files through these, with the user who asked. The core has asked PermissionPolicy.Rights before either.
type UserScopedStore interface {
	HeadFor(u User, key, revision string) (*ProjectHead, error)
	ReadFileFor(u User, key, revision, name string) (string, error)
}

// ListedProject is one entry of UserProjectLister.
type ListedProject struct {
	Info   ProjectInfo
	Rights ProjectRights
}

// UserProjectLister is optional for a ProjectStore: it lists only the projects one user may open, with that user's
// rights, so project/list does not ask Rights for every project of a large store.
type UserProjectLister interface {
	ListFor(u User) ([]ListedProject, error)
}

// ---------------------------------------------------------------- operator hooks

// SessionHooks is optional: it lets an operator follow hosted sessions (dzbot holds the project while one runs).
type SessionHooks interface {
	// HostedStarting is asked in session/create before the session exists. An error refuses the create; a
	// *protocol.Error goes to the client as it is (already_running with sessionId, not_allowed).
	HostedStarting(sessionID string, ref HostedRef, revision string, u User) error
	// HostedEnded is told once a hosted session has ended, for any reason. It runs outside the core's lock.
	HostedEnded(sessionID string, ref HostedRef)
}

// ---------------------------------------------------------------- clock

// Clock gives milliseconds; only differences matter.
type Clock interface {
	Now() int64
}

// Logger is optional.
type Logger interface {
	Info(msg string)
	Error(msg string, err error)
}
