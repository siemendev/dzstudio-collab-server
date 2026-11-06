// Package configperms gives rights and the user directory from the config file.
//
// Principals: "*" (every logged-in user), a userId, or "name:<display name>".
package configperms

import (
	"sort"
	"strings"

	"github.com/siemendev/dzstudio-collab-server/core"
)

type HostedRule struct {
	Provider string   `json:"provider"`
	Key      string   `json:"key"` // project key or "*"
	Join     []string `json:"join"`
	Save     []string `json:"save"`
	Manage   []string `json:"manage"`
	Invite   []string `json:"invite"` // defaults to join
}

type Config struct {
	HostLocal       []string            `json:"hostLocal"`
	LocalAlwaysJoin map[string][]string `json:"localAlwaysJoin"`
	Hosted          []HostedRule        `json:"hosted"`
}

func Matches(u core.User, principals []string) bool {
	for _, p := range principals {
		if p == "*" || p == u.UserID {
			return true
		}
		if name, ok := strings.CutPrefix(p, "name:"); ok && name == u.Name {
			return true
		}
	}
	return false
}

type Permissions struct{ cfg Config }

func New(cfg Config) *Permissions {
	if cfg.HostLocal == nil {
		cfg.HostLocal = []string{"*"}
	}
	return &Permissions{cfg}
}

func (p *Permissions) MayHostLocal(u core.User) (bool, error) {
	return Matches(u, p.cfg.HostLocal), nil
}

func (p *Permissions) Rights(u core.User, pc core.ProjectContext) (core.ProjectRights, error) {
	var r core.ProjectRights
	if pc.Kind == "local" {
		for host, users := range p.cfg.LocalAlwaysJoin {
			if Matches(pc.Host, []string{host}) && Matches(u, users) {
				r.Join, r.Invite = true, true
			}
		}
		return r, nil
	}
	for _, rule := range p.cfg.Hosted {
		if rule.Provider != pc.HostedRef.Provider || (rule.Key != "*" && rule.Key != pc.HostedRef.Key) {
			continue
		}
		save := Matches(u, rule.Save)
		manage := Matches(u, rule.Manage)
		join := save || manage || Matches(u, rule.Join)
		r.Join = r.Join || join
		r.Save = r.Save || save
		r.Manage = r.Manage || manage
		if rule.Invite != nil {
			r.Invite = r.Invite || Matches(u, rule.Invite)
		} else {
			r.Invite = r.Invite || join
		}
	}
	return r, nil
}

// MayCreate: a rule for every key of the provider lets its savers create new projects there.
func (p *Permissions) MayCreate(u core.User, provider string) (bool, error) {
	for _, rule := range p.cfg.Hosted {
		if rule.Provider == provider && rule.Key == "*" && Matches(u, rule.Save) {
			return true, nil
		}
	}
	return false, nil
}

type DirectoryConfig struct {
	Mode    string              `json:"mode"` // all, friends, none
	Friends map[string][]string `json:"friends"`
}

// Directory: mode all lists configured users and everyone seen in the last day; friends the lists in the config.
type Directory struct {
	cfg   DirectoryConfig
	known func() []core.User
}

func NewDirectory(cfg DirectoryConfig, known func() []core.User) *Directory {
	return &Directory{cfg, known}
}

func (d *Directory) ListAvailable(u core.User) ([]core.User, error) {
	all := map[string]core.User{}
	for _, k := range d.known() {
		all[k.UserID] = k
	}
	var out []core.User
	if d.cfg.Mode == "friends" {
		ids := map[string]bool{}
		for pattern, list := range d.cfg.Friends {
			if Matches(u, []string{pattern}) {
				for _, id := range list {
					ids[id] = true
				}
			}
		}
		delete(ids, u.UserID)
		for id := range ids {
			if k, ok := all[id]; ok {
				out = append(out, k)
			} else {
				out = append(out, core.User{UserID: id, Name: id})
			}
		}
	} else {
		delete(all, u.UserID)
		for _, k := range all {
			out = append(out, k)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}
