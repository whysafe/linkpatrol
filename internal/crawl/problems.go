package crawl

import (
	"fmt"
	"net/url"
	"sort"
	"strings"
)

// Severity of a finding.
const (
	Error   = "error"
	Warning = "warning"
)

// Problem is one finding on one page.
type Problem struct {
	Page     string `json:"page"`
	Link     string `json:"link"`
	Kind     string `json:"kind"`     // broken, anchor, redirect, mixed, robots
	Severity string `json:"severity"` // error or warning
	Detail   string `json:"detail"`
	Element  string `json:"element"` // a, img, script, ...
	Text     string `json:"text,omitempty"`
}

// Problems turns raw results into findings. Duplicates (same page, link and kind) are merged.
func (r *Report) Problems() []Problem {
	var out []Problem
	seen := map[string]bool{}
	add := func(p Problem) {
		key := p.Page + "\x00" + p.Link + "\x00" + p.Kind
		if !seen[key] {
			seen[key] = true
			out = append(out, p)
		}
	}
	for _, ref := range r.Refs {
		res := r.Results[ref.Link.URL.String()]
		link := ref.Link.Raw
		base := Problem{Page: ref.Page, Link: link, Element: ref.Link.Kind, Text: ref.Link.Text}

		if strings.HasPrefix(ref.Page, "https://") && ref.Link.URL.Scheme == "http" && ref.Link.Kind != "a" {
			p := base
			p.Kind, p.Severity = "mixed", Error
			p.Detail = "HTTP resource on an HTTPS page: browsers block or warn about it"
			add(p)
		}
		if res == nil {
			continue // external link while external checks are off
		}
		switch {
		case res.Skipped != "":
			p := base
			p.Kind, p.Severity, p.Detail = "robots", Warning, "not checked: disallowed by robots.txt"
			add(p)
		case res.Broken():
			p := base
			p.Kind, p.Severity = "broken", Error
			if res.Error != "" {
				p.Detail = res.Error
			} else {
				p.Detail = fmt.Sprintf("HTTP %d", res.Status)
			}
			add(p)
		default:
			if len(res.Redirects) > 0 {
				p := base
				p.Kind, p.Severity = "redirect", Warning
				p.Detail = fmt.Sprintf("%d redirect(s), final URL: %s", len(res.Redirects), res.Redirects[len(res.Redirects)-1])
				add(p)
			}
			if anchorLike(ref.Link.Frag) && res.parsed && !res.ids[ref.Link.Frag] && !res.ids[decodeFrag(ref.Link.Frag)] {
				p := base
				p.Kind, p.Severity = "anchor", Error
				p.Detail = fmt.Sprintf("no element with id %q on the target page", ref.Link.Frag)
				add(p)
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Page != out[j].Page {
			return out[i].Page < out[j].Page
		}
		if out[i].Severity != out[j].Severity {
			return out[i].Severity == Error
		}
		return out[i].Link < out[j].Link
	})
	return out
}

// anchorLike tells real anchors from client-side routes: SPAs with hash
// routing use #/cart or #!/cart, and those never match an element id.
func anchorLike(frag string) bool {
	return frag != "" && !strings.HasPrefix(frag, "/") && !strings.HasPrefix(frag, "!")
}

func decodeFrag(f string) string {
	if s, err := url.PathUnescape(f); err == nil {
		return s
	}
	return f
}
