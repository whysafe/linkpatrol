package crawl

import (
	"bufio"
	"io"
	"strings"
)

// Robots holds the Disallow/Allow rules that apply to our user agent.
// Only the parts that matter for a link checker are supported: groups by
// User-agent, Allow/Disallow with prefix matching and the longest match wins.
type Robots struct {
	rules []rule
}

type rule struct {
	allow bool
	path  string
}

// ParseRobots reads robots.txt and keeps the group for agent, or for "*" if
// there is no group naming the agent explicitly.
func ParseRobots(r io.Reader, agent string) *Robots {
	agent = strings.ToLower(agent)
	type group struct {
		agents []string
		rules  []rule
	}
	var groups []*group
	var cur *group
	lastWasAgent := false
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := sc.Text()
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key = strings.ToLower(strings.TrimSpace(key))
		val = strings.TrimSpace(val)
		switch key {
		case "user-agent":
			if !lastWasAgent || cur == nil {
				cur = &group{}
				groups = append(groups, cur)
			}
			cur.agents = append(cur.agents, strings.ToLower(val))
			lastWasAgent = true
		case "allow", "disallow":
			lastWasAgent = false
			if cur == nil || (key == "disallow" && val == "") {
				continue
			}
			cur.rules = append(cur.rules, rule{allow: key == "allow", path: val})
		default:
			lastWasAgent = false
		}
	}
	var star, own *group
	for _, g := range groups {
		for _, a := range g.agents {
			if a == "*" && star == nil {
				star = g
			} else if a != "*" && strings.Contains(agent, a) && own == nil {
				own = g
			}
		}
	}
	if own == nil {
		own = star
	}
	if own == nil {
		return &Robots{}
	}
	return &Robots{rules: own.rules}
}

// Allowed reports whether path (with query) may be fetched.
func (r *Robots) Allowed(path string) bool {
	if r == nil {
		return true
	}
	best := -1
	allowed := true
	for _, ru := range r.rules {
		if strings.HasPrefix(path, ru.path) && len(ru.path) > best {
			best = len(ru.path)
			allowed = ru.allow
		} else if strings.HasPrefix(path, ru.path) && len(ru.path) == best && ru.allow {
			allowed = true // on a tie, Allow wins (Google's rule)
		}
	}
	return allowed
}
