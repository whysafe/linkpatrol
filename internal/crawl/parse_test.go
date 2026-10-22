package crawl

import (
	"net/url"
	"strings"
	"testing"
)

func TestParseExtractsLinksAndAnchors(t *testing.T) {
	base, _ := url.Parse("https://example.com/docs/page.html")
	html := `<html><head>
		<link rel="stylesheet" href="/style.css">
		<link rel="preconnect" href="https://fonts.gstatic.com">
		<script src="app.js"></script>
	</head><body>
		<h2 id="intro">Intro</h2><a name="legacy"></a>
		<a href="../guide/#setup">Guide <b>setup</b></a>
		<a href="mailto:a@b.c">mail</a><a href="javascript:void(0)">js</a><a href="tel:+7">tel</a>
		<img src="https://cdn.example.com/i.png" alt="Diagram">
		<a href="  https://other.site/x  ">spaces</a>
		<a href="ftp://files.example.com">ftp</a>
	</body></html>`
	p, err := Parse(strings.NewReader(html), base)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, l := range p.Links {
		got = append(got, l.Kind+" "+l.URL.String()+"#"+l.Frag)
	}
	want := []string{
		"link https://example.com/style.css#",
		"script https://example.com/docs/app.js#",
		"a https://example.com/guide/#setup",
		"img https://cdn.example.com/i.png#",
		"a https://other.site/x#",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("links:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if !p.IDs["intro"] || !p.IDs["legacy"] {
		t.Errorf("ids = %v", p.IDs)
	}
	if p.Links[2].Text != "Guide setup" || p.Links[3].Text != "Diagram" {
		t.Errorf("labels: %q, %q", p.Links[2].Text, p.Links[3].Text)
	}
}

func TestBaseHrefChangesResolution(t *testing.T) {
	base, _ := url.Parse("https://example.com/a/b.html")
	p, _ := Parse(strings.NewReader(`<base href="https://static.example.com/v2/"><a href="x.html">x</a>`), base)
	if got := p.Links[0].URL.String(); got != "https://static.example.com/v2/x.html" {
		t.Errorf("got %s", got)
	}
}

func TestRobots(t *testing.T) {
	txt := `
# comment
User-agent: Googlebot
Disallow: /

User-agent: *
Disallow: /admin
Disallow: /tmp/
Allow: /admin/public
Disallow:
`
	r := ParseRobots(strings.NewReader(txt), "linkpatrol/1.0")
	cases := map[string]bool{
		"/":                 true,
		"/admin":            false,
		"/admin/settings":   false,
		"/admin/public/faq": true,
		"/tmp/x":            false,
		"/tmpfile":          true,
	}
	for path, want := range cases {
		if got := r.Allowed(path); got != want {
			t.Errorf("Allowed(%q) = %v, want %v", path, got, want)
		}
	}
	google := ParseRobots(strings.NewReader(txt), "Mozilla/5.0 (compatible; Googlebot/2.1)")
	if google.Allowed("/anything") {
		t.Error("the Googlebot group must apply to Googlebot")
	}
	if !(*Robots)(nil).Allowed("/x") {
		t.Error("nil robots allows everything")
	}
}

func TestAnchorLike(t *testing.T) {
	for frag, want := range map[string]bool{"top": true, "раздел": true, "": false, "/cart": false, "!/cart": false} {
		if anchorLike(frag) != want {
			t.Errorf("anchorLike(%q) != %v", frag, want)
		}
	}
}

func TestIndexHTMLIsTheSamePageAsTheDirectory(t *testing.T) {
	start, _ := url.Parse("https://example.com/")
	c := &crawler{cfg: Config{Start: start}}
	for in, want := range map[string]string{
		"https://example.com":                "https://example.com/",
		"https://example.com/index.html":     "https://example.com/",
		"https://example.com/blog/index.htm": "https://example.com/blog/",
		"https://example.com/index.html?a=1": "https://example.com/?a=1",
		"https://example.com/myindex.html":   "https://example.com/myindex.html",
		"https://other.example/index.html":   "https://other.example/index.html",
	} {
		u, _ := url.Parse(in)
		if got := c.canonical(u).String(); got != want {
			t.Errorf("canonical(%s) = %s, want %s", in, got, want)
		}
	}
}
