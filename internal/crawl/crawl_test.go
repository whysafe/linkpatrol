package crawl

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// site serves a small website with known problems.
func site(t *testing.T, external string) (*httptest.Server, *int64) {
	t.Helper()
	var hits int64
	mux := http.NewServeMux()
	page := func(body string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt64(&hits, 1)
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprint(w, "<!doctype html><html><body>"+body+"</body></html>")
		}
	}
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		page(`
			<a href="/about">О нас</a>
			<a href="/about#team">Команда</a>
			<a href="/about#nope">Нет якоря</a>
			<a href="/missing">Битая</a>
			<a href="/old">Старая ссылка</a>
			<a href="/private/report">Закрыто</a>
			<a href="#/cart">Корзина SPA</a>
			<a href="mailto:hi@example.com">Почта</a>
			<a href="`+external+`/ok">Внешняя</a>
			<a href="`+external+`/gone">Внешняя битая</a>
			<img src="/logo.png" alt="Логотип">
			<img src="/no-image.png" alt="Нет картинки">
		`)(w, r)
	})
	mux.HandleFunc("/about", page(`<h2 id="team">Команда</h2><a href="/">На главную</a><a href="/deep">Глубже</a>`))
	mux.HandleFunc("/deep", page(`<a href="/deeper">Ещё глубже</a>`))
	mux.HandleFunc("/deeper", page(`<p>дно</p>`))
	mux.HandleFunc("/old", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/about", http.StatusMovedPermanently)
	})
	mux.HandleFunc("/logo.png", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Write([]byte("\x89PNG"))
	})
	mux.HandleFunc("/robots.txt", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "User-agent: *\nDisallow: /private/\n")
	})
	mux.HandleFunc("/private/report", page(`секрет`))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, &hits
}

// externalSite answers 200 for /ok and 404 for the rest; HEAD is not allowed, like on some real servers.
func externalSite(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if r.URL.Path == "/ok" {
			fmt.Fprint(w, "ok")
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func crawl(t *testing.T, cfg Config) *Report {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	rep, err := Run(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

func kinds(problems []Problem) map[string]string {
	out := map[string]string{}
	for _, p := range problems {
		out[p.Link] = p.Kind
	}
	return out
}

func TestFindsEveryKindOfProblem(t *testing.T) {
	ext := externalSite(t)
	srv, _ := site(t, ext.URL)
	start, _ := url.Parse(srv.URL + "/")
	rep := crawl(t, Config{Start: start, MaxDepth: -1, External: true, Workers: 4})

	got := kinds(rep.Problems())
	want := map[string]string{
		"/about#nope":     "anchor",
		"/missing":        "broken",
		"/old":            "redirect",
		"/private/report": "robots",
		"/no-image.png":   "broken",
		ext.URL + "/gone": "broken",
	}
	for link, kind := range want {
		if got[link] != kind {
			t.Errorf("%s: got %q, want %q", link, got[link], kind)
		}
	}
	for _, ok := range []string{"/about", "/about#team", "#/cart", "/logo.png", ext.URL + "/ok"} {
		if k, bad := got[ok]; bad {
			t.Errorf("%s should be fine, got %q", ok, k)
		}
	}
	if rep.Pages != 4 { // /, /about, /deep, /deeper
		t.Errorf("pages = %d, want 4", rep.Pages)
	}
}

func TestExternalLinksAreSkippedWhenDisabled(t *testing.T) {
	ext := externalSite(t)
	srv, _ := site(t, ext.URL)
	start, _ := url.Parse(srv.URL + "/")
	rep := crawl(t, Config{Start: start, MaxDepth: -1, External: false})
	for _, p := range rep.Problems() {
		if strings.HasPrefix(p.Link, ext.URL) {
			t.Errorf("external link reported: %+v", p)
		}
	}
}

func TestDepthLimitsCrawlingButStillChecksLinks(t *testing.T) {
	srv, _ := site(t, "http://127.0.0.1:1")
	start, _ := url.Parse(srv.URL + "/")
	rep := crawl(t, Config{Start: start, MaxDepth: 1})
	if rep.Pages != 2 {
		t.Errorf("pages = %d, want 2 (start page and depth-1 pages only)", rep.Pages)
	}
	if res := rep.Results[srv.URL+"/deep"]; res == nil || res.Status != 200 {
		t.Errorf("/deep should still be checked: %+v", res)
	}
}

func TestEachURLIsFetchedOnce(t *testing.T) {
	srv, hits := site(t, "http://127.0.0.1:1")
	start, _ := url.Parse(srv.URL + "/")
	crawl(t, Config{Start: start, MaxDepth: -1, Workers: 8})
	// /, /about (also reached via /old and /about#team), /deep, /deeper, /missing, /private is skipped
	if n := atomic.LoadInt64(hits); n > 6 {
		t.Errorf("pages fetched %d times, duplicates are not deduplicated", n)
	}
}

func TestIgnoreRobotsCrawlsDisallowedPages(t *testing.T) {
	srv, _ := site(t, "http://127.0.0.1:1")
	start, _ := url.Parse(srv.URL + "/")
	rep := crawl(t, Config{Start: start, MaxDepth: -1, IgnoreRobots: true})
	if res := rep.Results[srv.URL+"/private/report"]; res == nil || res.Skipped != "" || res.Status != 200 {
		t.Errorf("robots.txt should be ignored: %+v", res)
	}
}

func TestMixedContentOnHTTPSPages(t *testing.T) {
	rep := &Report{Results: map[string]*Result{}}
	u, _ := url.Parse("http://cdn.example.com/lib.js")
	a, _ := url.Parse("http://example.com/page")
	rep.Refs = []Ref{
		{Page: "https://example.com/", Link: Link{URL: u, Raw: "http://cdn.example.com/lib.js", Kind: "script"}},
		{Page: "https://example.com/", Link: Link{URL: a, Raw: "http://example.com/page", Kind: "a"}},
	}
	got := kinds(rep.Problems())
	if got["http://cdn.example.com/lib.js"] != "mixed" {
		t.Errorf("script over http on https page must be mixed content, got %v", got)
	}
	if _, ok := got["http://example.com/page"]; ok {
		t.Errorf("plain links are not mixed content")
	}
}

func TestRateLimitSpacesRequests(t *testing.T) {
	l := newLimiter(20) // 50 ms apart
	start := time.Now()
	for i := 0; i < 5; i++ {
		l.wait(context.Background(), "example.com")
	}
	if d := time.Since(start); d < 190*time.Millisecond {
		t.Errorf("5 requests at 20 rps took %v, expected at least 200ms", d)
	}
}

func TestRejectsNonHTTPStart(t *testing.T) {
	u, _ := url.Parse("ftp://example.com")
	if _, err := Run(context.Background(), Config{Start: u}); err == nil {
		t.Error("expected an error for ftp://")
	}
}

func TestProblemsAreSortedErrorsFirst(t *testing.T) {
	ext := externalSite(t)
	srv, _ := site(t, ext.URL)
	start, _ := url.Parse(srv.URL + "/")
	problems := crawl(t, Config{Start: start, MaxDepth: -1, External: true}).Problems()
	ok := sort.SliceIsSorted(problems, func(i, j int) bool {
		if problems[i].Page != problems[j].Page {
			return problems[i].Page < problems[j].Page
		}
		return problems[i].Severity == Error && problems[j].Severity != Error
	})
	if !ok {
		t.Error("problems are not grouped by page with errors first")
	}
}
