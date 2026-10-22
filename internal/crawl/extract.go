package crawl

import (
	"io"
	"net/url"
	"strings"

	"golang.org/x/net/html"
)

// Link is a reference found on a page.
type Link struct {
	URL  *url.URL // absolute, without fragment
	Raw  string   // as written in the HTML
	Frag string   // #fragment, if any
	Kind string   // a, img, script, link, iframe, source
	Text string   // anchor text or alt, for the report
}

// Page is what a crawled HTML document contains.
type Page struct {
	Links []Link
	IDs   map[string]bool // id and name attributes, to check #fragments
}

var linkAttrs = map[string]string{
	"a":      "href",
	"img":    "src",
	"script": "src",
	"link":   "href",
	"iframe": "src",
	"source": "src",
}

// Parse extracts links and anchor targets. base is the page URL; a <base href> overrides it.
func Parse(r io.Reader, base *url.URL) (*Page, error) {
	doc, err := html.Parse(r)
	if err != nil {
		return nil, err
	}
	p := &Page{IDs: map[string]bool{}}
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			for _, a := range n.Attr {
				if a.Key == "id" || (a.Key == "name" && n.Data == "a") {
					p.IDs[a.Val] = true
				}
			}
			if n.Data == "base" {
				if b, err := base.Parse(attr(n, "href")); err == nil && attr(n, "href") != "" {
					base = b
				}
			}
			if key, ok := linkAttrs[n.Data]; ok {
				if raw := strings.TrimSpace(attr(n, key)); raw != "" && !skip(n, raw) {
					if u, err := base.Parse(raw); err == nil && (u.Scheme == "http" || u.Scheme == "https") {
						frag := u.Fragment
						u.Fragment = ""
						u.RawFragment = ""
						p.Links = append(p.Links, Link{URL: u, Raw: raw, Frag: frag, Kind: n.Data, Text: label(n)})
					}
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	return p, nil
}

func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

// skip drops links that are not resources: mailto, tel, javascript, data and preconnect hints.
func skip(n *html.Node, raw string) bool {
	lower := strings.ToLower(raw)
	for _, p := range []string{"mailto:", "tel:", "javascript:", "data:", "sms:"} {
		if strings.HasPrefix(lower, p) {
			return true
		}
	}
	if n.Data == "link" {
		switch strings.ToLower(attr(n, "rel")) {
		case "preconnect", "dns-prefetch", "canonical", "alternate":
			return true
		}
	}
	return false
}

func label(n *html.Node) string {
	if alt := attr(n, "alt"); alt != "" {
		return alt
	}
	var b strings.Builder
	var text func(*html.Node)
	text = func(c *html.Node) {
		if c.Type == html.TextNode {
			b.WriteString(c.Data)
		}
		for k := c.FirstChild; k != nil; k = k.NextSibling {
			text(k)
		}
	}
	text(n)
	s := strings.Join(strings.Fields(b.String()), " ")
	if r := []rune(s); len(r) > 60 {
		s = string(r[:57]) + "..."
	}
	return s
}
