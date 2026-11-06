// Package config reads the reference server's JSON config file on top of the defaults.
package config

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/siemendev/dzstudio-collab-server/adapters/configperms"
	"github.com/siemendev/dzstudio-collab-server/adapters/localidentity"
	"github.com/siemendev/dzstudio-collab-server/protocol"
)

type Store struct {
	Provider string `json:"provider"`
	Root     string `json:"root"`
	Map      string `json:"map"` // map key for projects whose name and map are not recorded
}

type Config struct {
	Server struct {
		Name   string `json:"name"`
		Notice string `json:"notice"`
	} `json:"server"`
	Listen struct {
		Host     string `json:"host"`
		Port     int    `json:"port"`
		BasePath string `json:"basePath"`
	} `json:"listen"`
	PublicURL  string `json:"publicUrl"`
	TrustProxy bool   `json:"trustProxy"`
	DataDir    string `json:"dataDir"`
	Storage    struct {
		Type  string `json:"type"` // file or memory
		Fsync bool   `json:"fsync"`
	} `json:"storage"`
	Auth struct {
		Secret          string `json:"secret"`
		TokenTTLSeconds int64  `json:"tokenTtlSeconds"`
		Name            struct {
			Enabled      bool     `json:"enabled"`
			AllowedNames []string `json:"allowedNames"`
		} `json:"name"`
		Code struct {
			Enabled          bool  `json:"enabled"`
			ExpiresInSeconds int64 `json:"expiresInSeconds"`
			IntervalSeconds  int64 `json:"intervalSeconds"`
		} `json:"code"`
	} `json:"auth"`
	Users       []localidentity.ConfiguredUser `json:"users"`
	Permissions configperms.Config             `json:"permissions"`
	Directory   configperms.DirectoryConfig    `json:"directory"`
	InviteUser  bool                           `json:"inviteUser"`
	Hosted      struct {
		Enabled bool    `json:"enabled"`
		Stores  []Store `json:"stores"`
	} `json:"hosted"`
	Limits struct {
		MaxRequestBytes  int64 `json:"maxRequestBytes"`
		MaxParticipants  int   `json:"maxParticipants"`
		MaxSnapshotBytes int64 `json:"maxSnapshotBytes"`
		CheckpointOps    int64 `json:"checkpointOps"`
		CheckpointBytes  int64 `json:"checkpointBytes"`
		MaxLocalPerUser  int   `json:"maxLocalSessionsPerUser"`
		MaxHostedPerUser int   `json:"maxHostedSessionsPerUser"`
	} `json:"limits"`
	AllowedClients []string `json:"allowedClients"`
	ClientVersion  struct {
		Min         string `json:"min"`
		Recommended string `json:"recommended"`
		UpdateURL   string `json:"updateUrl"`
	} `json:"clientVersion"`
	Testing struct {
		Enabled bool `json:"enabled"`
	} `json:"testing"`
}

func Default() *Config {
	c := &Config{}
	c.Server.Name = "dzStudio collaboration server"
	c.Listen.Host = "0.0.0.0"
	c.Listen.Port = 8080
	c.Listen.BasePath = "/dzcollab/"
	c.DataDir = "./data"
	c.Storage.Type = "file"
	c.Auth.TokenTTLSeconds = 30 * 24 * 3600
	c.Auth.Name.Enabled = true
	c.Auth.Code.ExpiresInSeconds = 600
	c.Auth.Code.IntervalSeconds = 3
	c.Permissions.HostLocal = []string{"*"}
	c.Directory.Mode = "all"
	c.InviteUser = true
	c.Hosted.Stores = []Store{{Provider: "fs", Root: "./projects"}}
	c.Limits.MaxRequestBytes = 8_388_608
	c.Limits.MaxParticipants = 8
	c.Limits.MaxSnapshotBytes = 64 * 1_048_576
	c.Limits.CheckpointOps = 1000
	c.Limits.CheckpointBytes = 8 * 1_048_576
	return c
}

// Load reads path (may be empty) on top of the defaults; relative paths are relative to the file. The environment
// variables DZCOLLAB_PORT, DZCOLLAB_DATA_DIR and DZCOLLAB_PUBLIC_URL override the file.
func Load(path string) (*Config, error) {
	c := Default()
	base, _ := os.Getwd()
	if path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(data, c); err != nil {
			return nil, fmt.Errorf("config %s: %w", path, err)
		}
		abs, _ := filepath.Abs(path)
		base = filepath.Dir(abs)
	}
	if v := os.Getenv("DZCOLLAB_PORT"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return nil, fmt.Errorf("DZCOLLAB_PORT: %w", err)
		}
		c.Listen.Port = n
	}
	if v := os.Getenv("DZCOLLAB_DATA_DIR"); v != "" {
		c.DataDir = v
	}
	if v := os.Getenv("DZCOLLAB_PUBLIC_URL"); v != "" {
		c.PublicURL = v
	}
	abs := func(p string) string {
		if filepath.IsAbs(p) {
			return p
		}
		return filepath.Join(base, p)
	}
	c.DataDir = abs(c.DataDir)
	for i := range c.Hosted.Stores {
		c.Hosted.Stores[i].Root = abs(c.Hosted.Stores[i].Root)
	}
	return c, c.Validate()
}

var idRE = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

func (c *Config) Validate() error {
	var errs []string
	if c.Server.Name == "" || len(c.Server.Name) > 64 {
		errs = append(errs, "server.name must be 1–64 bytes")
	}
	if len(c.Server.Notice) > 1000 {
		errs = append(errs, "server.notice must be at most 1000 bytes")
	}
	ids := map[string]bool{}
	for _, u := range c.Users {
		if !idRE.MatchString(u.ID) {
			errs = append(errs, fmt.Sprintf("users: id %q must be 1–64 characters A–Z a–z 0–9 _ -", u.ID))
		}
		if strings.HasPrefix(u.ID, "n-") {
			errs = append(errs, fmt.Sprintf("users: id %q must not start with n- (reserved for name logins)", u.ID))
		}
		if u.Name == "" || len(u.Name) > 64 {
			errs = append(errs, fmt.Sprintf("users: name of %q must be 1–64 bytes", u.ID))
		}
		if ids[u.ID] {
			errs = append(errs, "users: ids must be unique")
		}
		ids[u.ID] = true
	}
	if !c.Auth.Name.Enabled && !c.Auth.Code.Enabled {
		errs = append(errs, "auth: enable at least one of name and code")
	}
	if c.Auth.Code.Enabled {
		has := false
		for _, u := range c.Users {
			has = has || u.Password != ""
		}
		if !has {
			errs = append(errs, "auth.code needs users with a password")
		}
		if c.Auth.Code.IntervalSeconds < 2 {
			errs = append(errs, "auth.code.intervalSeconds must be at least 2")
		}
	}
	if c.Auth.Secret != "" && len(c.Auth.Secret) < 32 {
		errs = append(errs, "auth.secret must have at least 32 characters")
	}
	if c.Limits.MaxRequestBytes < 4_194_304 {
		errs = append(errs, "limits.maxRequestBytes must be at least 4194304")
	}
	if c.Limits.MaxParticipants < 2 {
		errs = append(errs, "limits.maxParticipants must be at least 2")
	}
	if c.Storage.Type != "file" && c.Storage.Type != "memory" {
		errs = append(errs, "storage.type must be file or memory")
	}
	switch c.Directory.Mode {
	case "all", "friends", "none":
	default:
		errs = append(errs, "directory.mode must be all, friends or none")
	}
	if v := c.ClientVersion.Min; v != "" {
		if _, ok := protocol.ParseClientVersion(v); !ok {
			errs = append(errs, "clientVersion.min must look like 0.4.0 (MAJOR.MINOR.PATCH, each 0–999)")
		}
	}
	if v := c.ClientVersion.Recommended; v != "" {
		if _, ok := protocol.ParseClientVersion(v); !ok {
			errs = append(errs, "clientVersion.recommended must look like 0.4.0 (MAJOR.MINOR.PATCH, each 0–999)")
		}
	}
	if u := c.ClientVersion.UpdateURL; u != "" && (len(u) > 1000 || !(strings.HasPrefix(u, "https://") || strings.HasPrefix(u, "http://"))) {
		errs = append(errs, "clientVersion.updateUrl must be an http(s) address of at most 1000 bytes")
	}
	if c.Hosted.Enabled && len(c.Hosted.Stores) == 0 {
		errs = append(errs, "hosted.stores must name at least one store")
	}
	if len(errs) > 0 {
		return errors.New("invalid configuration:\n  " + strings.Join(errs, "\n  "))
	}
	return nil
}

// TokenSecret is the configured secret, or one generated once and kept in the data directory.
func (c *Config) TokenSecret() (string, error) {
	if c.Auth.Secret != "" {
		return c.Auth.Secret, nil
	}
	file := filepath.Join(c.DataDir, "token-secret")
	if data, err := os.ReadFile(file); err == nil {
		if s := strings.TrimSpace(string(data)); len(s) >= 32 {
			return s, nil
		}
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	secret := base64.RawURLEncoding.EncodeToString(b)
	if err := os.MkdirAll(c.DataDir, 0o755); err != nil {
		return "", err
	}
	return secret, os.WriteFile(file, []byte(secret+"\n"), 0o600)
}
