package filestore

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
)

// Attachments keeps attachment parts as <root>/<sessionId>/attachments/<sha256 of the checksum>/<part>.json, a JSON
// list of the pieces. It lives inside the session directories of Store, so deleting a session removes them too.
type Attachments struct {
	root  string
	fsync bool
	w     *Store
}

func NewAttachments(root string, fsync bool) *Attachments {
	return &Attachments{root: root, fsync: fsync, w: &Store{root: root, fsync: fsync}}
}

func (a *Attachments) dir(sessionID, checksum string) (string, error) {
	if !safe.MatchString(sessionID) {
		return "", fmt.Errorf("unsafe session id %q", sessionID)
	}
	h := sha256.Sum256([]byte(checksum))
	return filepath.Join(a.root, sessionID, "attachments", hex.EncodeToString(h[:])), nil
}

func (a *Attachments) PutPart(sessionID, checksum string, part int, pieces []string) error {
	dir, err := a.dir(sessionID, checksum)
	if err != nil {
		return err
	}
	if pieces == nil {
		pieces = []string{}
	}
	data, err := json.Marshal(pieces)
	if err != nil {
		return err
	}
	return a.w.writeAtomic(filepath.Join(dir, strconv.Itoa(part)+".json"), data)
}

func (a *Attachments) GetPart(sessionID, checksum string, part int) ([]string, bool, error) {
	dir, err := a.dir(sessionID, checksum)
	if err != nil {
		return nil, false, err
	}
	data, err := os.ReadFile(filepath.Join(dir, strconv.Itoa(part)+".json"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	var pieces []string
	if err := json.Unmarshal(data, &pieces); err != nil {
		return nil, false, err
	}
	return pieces, true, nil
}

func (a *Attachments) Drop(sessionID, checksum string) error {
	dir, err := a.dir(sessionID, checksum)
	if err != nil {
		return err
	}
	return os.RemoveAll(dir)
}

func (a *Attachments) DropSession(sessionID string) error {
	if !safe.MatchString(sessionID) {
		return fmt.Errorf("unsafe session id %q", sessionID)
	}
	return os.RemoveAll(filepath.Join(a.root, sessionID, "attachments"))
}
