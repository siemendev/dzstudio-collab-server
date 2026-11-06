package core

type Requirement struct {
	Type    string `json:"type"`
	Version int64  `json:"version"`
}

type liveValue struct {
	Element  string
	Position [3]float64
	YPR      [3]float64
	Scale    float64
}

type liveTransform struct {
	Pivot     [3]float64
	Translate [3]float64
	Yaw       float64
}

type liveState struct {
	LiveID      string
	N           int64
	Mode        string
	Values      []liveValue
	Transform   *liveTransform
	Committed   bool
	CommittedAt int64
}

type notice struct {
	Kind          string
	Checksum      string
	By            string
	Count         int
	ParticipantID string
	Name          string
}

type numberedNotice struct {
	version int64
	n       notice
}

// pendingOp collects the parts of one operation (or one chain link) over 1 MB.
type pendingOp struct {
	ClientSeq int64
	OpID      string
	System    bool
	LiveID    string
	Chain     *Chain
	Parts     [][]string
	Have      []bool
	Bytes     int
}

type chainLink struct {
	ClientSeq int64
	Parts     [][]string
}

// openChain holds the accepted links of a chain until its last one is complete.
type openChain struct {
	OpID   string
	Count  int
	System bool
	LiveID string
	Links  []chainLink
}

type lockAnswer struct {
	LockSeq int64        `json:"lockSeq"`
	Granted []string     `json:"granted"`
	Denied  []lockDenial `json:"denied"`
	Version int64        `json:"version"`
}

type lockDenial struct {
	Element string  `json:"element"`
	Holder  *string `json:"holder"`
}

type participant struct {
	// persisted
	ID          string `json:"participantId"`
	User        User   `json:"user"`
	Role        string `json:"role"`
	Colour      int    `json:"colour"`
	CanInvite   bool   `json:"canInvite"`
	CanSave     bool   `json:"canSave"`
	CanManage   bool   `json:"canManage"`
	ViaInvite   bool   `json:"viaInvite"`
	InstanceKey string `json:"instanceKey"`
	JoinOrder   int64  `json:"joinOrder"`
	Visible     bool   `json:"visible"`
	Status      string `json:"status"` // active, left, removed, timeout
	RemovedBy   string `json:"removedBy,omitempty"`
	Cursor      int64  `json:"cursor"`
	NextSeq     int64  `json:"nextClientSeq"`
	LockSeq     int64  `json:"lockSeq"`
	NoticeVer   int64  `json:"noticeVersion"`
	Kind        string `json:"kind,omitempty"` // "agent", or empty for DZSTUDIO
	AgentName   string `json:"agentName,omitempty"`

	// runtime
	phase          string
	status         string
	openRequests   int
	lastContactEnd int64
	position       *[3]float64
	idleReported   float64
	idleReportedAt int64
	clientSeqToSeq map[int64]int64
	pending        *pendingOp
	chain          *openChain
	staged         []StagedItem
	lastLockAnswer *lockAnswer
	locks          map[string]struct{}
	lapsed         bool
	live           *liveState
	lastLiveAt     int64
	hasLastLive    bool
	lastLiveID     string
	lastLiveN      int64
	notices        []numberedNotice
	checkpointSent bool
	disconnectDone bool
	cut            chan struct{}
}

type snapshotInfo struct {
	Seq       int64 `json:"seq"`
	Parts     int   `json:"parts"`
	Bytes     int64 `json:"bytes"`
	NS        int64 `json:"ns"`
	KeepUntil int64 `json:"keepUntil,omitempty"`
}

type upload struct {
	Seq      int64        `json:"seq"`
	Parts    int          `json:"parts"`
	Received map[int]bool `json:"received"`
	Bytes    int64        `json:"bytes"`
	By       string       `json:"by"`
}

type invite struct {
	Code       string `json:"code"`
	SessionID  string `json:"sessionId"`
	CreatedAt  int64  `json:"createdAt"`
	ExpiresAt  int64  `json:"expiresAt"`
	CreatedBy  string `json:"createdBy"`
	ForUserID  string `json:"forUserId,omitempty"`
	RedeemedBy string `json:"redeemedBy,omitempty"`
	Invalid    bool   `json:"invalid,omitempty"`
}

type localSaving struct {
	State        string `json:"state"`
	UnsavedCount int64  `json:"unsavedCount"`
	SavedAt      *int64 `json:"savedAt,omitempty"`
}

type hostedSaving struct {
	SavedSeq        int64  `json:"savedSeq"`
	SavedNS         int64  `json:"savedNs"`
	SavedBy         *User  `json:"savedBy,omitempty"`
	SavedAt         *int64 `json:"savedAt,omitempty"`
	BaseRevision    string `json:"baseRevision"`
	CreatedRevision string `json:"createdRevision"`
	writing         bool
}

type ended struct {
	Reason string `json:"reason"`
	By     string `json:"by,omitempty"`
	At     int64  `json:"at"`
}

type versions struct {
	Participants int64 `json:"participants"`
	Locks        int64 `json:"locks"`
	Live         int64 `json:"live"`
	Saving       int64 `json:"saving"`
}

type session struct {
	ID                    string
	Kind                  string
	ProjectName           string
	Map                   string
	Requires              []Requirement
	Host                  *User
	HostedRef             *HostedRef
	CreatedAt             int64
	CreatorID             string
	ExpectedSnapshotParts int

	participants map[string]*participant
	nextPartNo   int64
	joinCounter  int64

	head             int64 // highest seq handed out
	committed        int64 // highest seq durably appended
	ns               int64
	truncatedThrough int64
	opIDs            map[string]int64
	opsSinceSnap     int64
	bytesSinceSnap   int64

	snapshot      *snapshotInfo
	previousSnaps []snapshotInfo
	uploads       map[int64]*upload

	attachments         map[string]*attachment
	attachmentUploaders map[string]string
	wanted              map[string]int64

	ver        versions
	verCeiling int64
	local      localSaving
	hosted     *hostedSaving

	lockOwner map[string]string
	lockCount int

	ended          *ended
	checkpointFrom string

	wake        chan struct{}
	recordDirty bool

	// lastActive: the last request of a participant; written with the record at most hourly (activeSaved).
	lastActive  int64
	activeSaved int64
}
