package core

import (
	"unicode"

	p "github.com/siemendev/dzstudio-collab-server/protocol"
)

const (
	maxStatusBytes    = 200
	maxAgentNameBytes = 64
)

// clientInfo is what session/create and session/join learn from client.
type clientInfo struct {
	agent     bool
	agentName string
}

// clientKind reads client.kind; a server without the capability agents ignores the field.
func (s *Service) clientKind(c *p.R) string {
	if !s.opts.Capabilities.Agents || !c.Has("kind") {
		return "studio"
	}
	return c.OneOf("kind", "studio", "agent")
}

func oneLine(text string) bool {
	for _, r := range text {
		if unicode.IsControl(r) || r == ' ' || r == ' ' {
			return false
		}
	}
	return true
}

func (s *Service) readAgentName(c *p.R) string {
	name, ok := c.OptString("agentName", maxAgentNameBytes)
	if !ok {
		return ""
	}
	if name == "" || !oneLine(name) {
		p.BadRequestf("client.agentName must be one line of 1–%d bytes", maxAgentNameBytes)
	}
	return name
}

// agentPresence is phase and status from an agent's poll; nil when the poll carries no presence.
type agentPresence struct {
	phase  string
	status string
}

func readAgentPresence(presence *p.R) *agentPresence {
	if presence == nil {
		return nil
	}
	phase := presence.OneOf("phase", "waiting", "working", "review")
	status, _ := presence.OptString("status", maxStatusBytes)
	if !oneLine(status) {
		p.BadRequestf("presence.status must be one line without control characters")
	}
	return &agentPresence{phase: phase, status: status}
}

func (pt *participant) isAgent() bool { return pt.Kind == "agent" }

// ownAgent: target is an agent of the same user as pt, who is not an agent; such a user may always remove it.
func ownAgent(pt, target *participant) bool {
	return target != nil && target.isAgent() && !pt.isAgent() && target.User.UserID == pt.User.UserID
}

func (s *Service) agentPhase(pt *participant) string {
	if pt.phase == "" {
		return "waiting"
	}
	return pt.phase
}
