package core

import (
	"errors"
	"strings"

	p "github.com/siemendev/dzstudio-collab-server/protocol"
)

// ClientVersions is the operator's rule for dzStudio's version (client.name "dzstudio").
type ClientVersions struct {
	Min         string // "" = no check
	Recommended string // counts only above Min
	UpdateURL   string // dzStudio's Workshop page, ≤ 1000 bytes
}

type clientPolicy struct {
	min, recommended       p.ClientVersion
	hasMin, hasRecommended bool
	updateURL              string
}

// clientVersionEndpoints are checked; session/join only without resume, so a running session never ends on the version.
var clientVersionEndpoints = map[string]bool{"auth/code/start": true, "auth/name": true, "session/create": true, "session/join": true}

func compileClientVersions(c ClientVersions) (*clientPolicy, error) {
	pol := &clientPolicy{updateURL: c.UpdateURL}
	if c.Min != "" {
		v, ok := p.ParseClientVersion(c.Min)
		if !ok {
			return nil, errors.New("minimum client version must look like 0.4.0")
		}
		pol.min, pol.hasMin = v, true
	}
	if c.Recommended != "" {
		v, ok := p.ParseClientVersion(c.Recommended)
		if !ok {
			return nil, errors.New("recommended client version must look like 0.4.0")
		}
		pol.recommended, pol.hasRecommended = v, pol.hasMin && pol.min.Less(v)
	}
	if c.UpdateURL != "" && (len(c.UpdateURL) > p.MaxTextBytes ||
		!(strings.HasPrefix(c.UpdateURL, "https://") || strings.HasPrefix(c.UpdateURL, "http://"))) {
		return nil, errors.New("update address must be an http(s) address of at most 1000 bytes")
	}
	return pol, nil
}

// SetClientVersions changes the rule while the server runs; it applies from the next request on.
func (s *Service) SetClientVersions(c ClientVersions) error {
	pol, err := compileClientVersions(c)
	if err != nil {
		return err
	}
	s.clientPolicy.Store(pol)
	return nil
}

func (s *Service) checkClientVersion(endpoint string, raw map[string]any) {
	pol := s.clientPolicy.Load()
	if !pol.hasMin || !clientVersionEndpoints[endpoint] || (endpoint == "session/join" && raw["resume"] != nil) {
		return
	}
	client, _ := raw["client"].(map[string]any)
	if name, ok := client["name"].(string); ok && name != "" && name != "dzstudio" {
		return
	}
	text, _ := client["version"].(string)
	if v, ok := p.ParseClientVersion(text); ok && !v.Less(pol.min) {
		return
	}
	your := p.TruncateUTF8(text, 32)
	minText := pol.min.String()
	msg := "This dzStudio is out of date, " + minText + " or newer is required."
	if your != "" {
		msg = "dzStudio " + your + " is out of date, " + minText + " or newer is required."
	}
	extra := []any{"minVersion", minText, "yourVersion", your}
	if pol.updateURL != "" {
		extra = append(extra, "updateUrl", pol.updateURL)
	}
	p.Fail(p.ClientOutdated, msg, extra...)
}

func (s *Service) setClientVersionInfo(out *p.Obj) {
	pol := s.clientPolicy.Load()
	if !pol.hasMin {
		return
	}
	out.Set("minClientVersion", pol.min.String())
	if pol.hasRecommended {
		out.Set("recommendedClientVersion", pol.recommended.String())
	}
	if pol.updateURL != "" {
		out.Set("updateUrl", pol.updateURL)
	}
}
