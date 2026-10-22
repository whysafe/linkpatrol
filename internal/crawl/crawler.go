// Package crawl walks a site and checks every link it finds.
package crawl

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

// Config controls a crawl.
type Config struct {
	Start        *url.URL
	Workers      int           // parallel requests
	MaxDepth     int           // how many clicks away from Start pages are crawled; -1 = no limit
	MaxPages     int           // safety limit for internal pages
	Timeout      time.Duration // per request
	External     bool          // also check links to other hosts
	RPS          float64       // requests per second per host; 0 = no limit
	UserAgent    string
	IgnoreRobots bool
	Client       *http.Client // optional, for tests
}

// Result is the outcome of fetching one URL.
type Result struct {
	URL       string   `json:"url"`
	Status    int      `json:"status,omitempty"`
	Error     string   `json:"error,omitempty"`
	Redirects []string `json:"redirects,omitempty"` // each hop after the original URL
	Skipped   string   `json:"skipped,omitempty"`   // e.g. "robots.txt"
	ids       map[string]bool
	parsed    bool
}

// Broken reports whether the link target is unusable.
func (r *Result) Broken() bool { return r.Error != "" || r.Status >= 400 }

// Ref is one link on one page.
type Ref struct {
	Page string
	Link Link
}

// Report is everything the crawl found.
type Report struct {
	Start    string
	Pages    int
	Checked  int
	Refs     []Ref
	Results  map[string]*Result
	Duration time.Duration
}

type task struct {
	u     *url.URL
	depth int
	page  bool // internal: fetch with GET and parse links
}

type crawler struct {
	cfg     Config
	client  *http.Client
	robots  *Robots
	limiter *limiter

	mu      sync.Mutex
	results map[string]*Result
	refs    []Ref
	pages   int // parsed HTML pages
	tried   int // internal pages fetched, for the MaxPages limit
	queued  map[string]bool
	wg      sync.WaitGroup
	tasks   chan task
}

// Run crawls the site and returns the report. The context cancels the crawl.
func Run(ctx context.Context, cfg Config) (*Report, error) {
	if cfg.Start == nil || (cfg.Start.Scheme != "http" && cfg.Start.Scheme != "https") {
		return nil, errors.New("start URL must be http or https")
	}
	if cfg.Workers <= 0 {
		cfg.Workers = 8
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 15 * time.Second
	}
	if cfg.MaxPages <= 0 {
		cfg.MaxPages = 5000
	}
	if cfg.UserAgent == "" {
		cfg.UserAgent = "linkpatrol/1.0 (+https://github.com/whysafe/linkpatrol)"
	}
	c := &crawler{
		cfg:     cfg,
		client:  cfg.Client,
		limiter: newLimiter(cfg.RPS),
		results: map[string]*Result{},
		queued:  map[string]bool{},
		tasks:   make(chan task, 1024),
	}
	if c.client == nil {
		c.client = &http.Client{Timeout: cfg.Timeout}
	}
	start := time.Now()
	if !cfg.IgnoreRobots {
		c.robots = c.fetchRobots(ctx)
	}

	for i := 0; i < cfg.Workers; i++ {
		go c.worker(ctx)
	}
	start0 := *cfg.Start
	start0.Fragment = ""
	c.enqueue(task{u: &start0, depth: 0, page: true})
	done := make(chan struct{})
	go func() { c.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
	}
	close(c.tasks)

	c.mu.Lock()
	defer c.mu.Unlock()
	sort.SliceStable(c.refs, func(i, j int) bool { return c.refs[i].Page < c.refs[j].Page })
	return &Report{
		Start: cfg.Start.String(), Pages: c.pages, Checked: len(c.results),
		Refs: c.refs, Results: c.results, Duration: time.Since(start),
	}, ctx.Err()
}

func (c *crawler) internal(u *url.URL) bool {
	return strings.EqualFold(u.Host, c.cfg.Start.Host)
}

// canonical maps dir/index.html to dir/ for internal pages: static hosts serve the
// same document for both, and without this every problem would be reported twice.
func (c *crawler) canonical(u *url.URL) *url.URL {
	if !c.internal(u) {
		return u
	}
	if u.Path == "" {
		v := *u
		v.Path = "/"
		return &v
	}
	for _, idx := range []string{"index.html", "index.htm"} {
		if strings.HasSuffix(u.Path, "/"+idx) {
			v := *u
			v.Path = strings.TrimSuffix(u.Path, idx)
			v.RawPath = ""
			return &v
		}
	}
	return u
}

func (c *crawler) enqueue(t task) {
	t.u = c.canonical(t.u)
	key := t.u.String()
	c.mu.Lock()
	if c.queued[key] {
		c.mu.Unlock()
		return
	}
	c.queued[key] = true
	c.wg.Add(1)
	c.mu.Unlock()
	go func() { c.tasks <- t }() // the channel never blocks a worker that enqueues
}

func (c *crawler) worker(ctx context.Context) {
	for t := range c.tasks {
		if ctx.Err() == nil {
			c.process(ctx, t)
		}
		c.wg.Done()
	}
}

func (c *crawler) process(ctx context.Context, t task) {
	res := &Result{URL: t.u.String()}
	if c.internal(t.u) && c.robots != nil && !c.robots.Allowed(t.u.RequestURI()) {
		res.Skipped = "robots.txt"
		c.store(res)
		return
	}
	crawlable := t.page && c.internal(t.u) && (c.cfg.MaxDepth < 0 || t.depth <= c.cfg.MaxDepth)
	if crawlable {
		c.mu.Lock()
		if c.tried >= c.cfg.MaxPages {
			crawlable = false
		} else {
			c.tried++
		}
		c.mu.Unlock()
	}

	var page *Page
	if crawlable {
		page = c.fetchPage(ctx, t, res)
	} else {
		c.check(ctx, t.u, res)
	}
	c.store(res)
	if page == nil {
		return
	}
	for _, l := range page.Links {
		l.URL = c.canonical(l.URL)
		c.mu.Lock()
		c.refs = append(c.refs, Ref{Page: t.u.String(), Link: l})
		c.mu.Unlock()
		if !c.internal(l.URL) && !c.cfg.External {
			continue
		}
		c.enqueue(task{u: l.URL, depth: t.depth + 1, page: l.Kind == "a" || l.Kind == "iframe"})
	}
}

func (c *crawler) store(res *Result) {
	c.mu.Lock()
	c.results[res.URL] = res
	c.mu.Unlock()
}

// fetchPage GETs an internal page; if it is HTML, its links and ids are parsed.
func (c *crawler) fetchPage(ctx context.Context, t task, res *Result) *Page {
	resp, err := c.do(ctx, http.MethodGet, t.u, res)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	// After a redirect the final page is crawled under its own URL, once, instead of
	// being parsed again for every old address that points to it.
	if final := *resp.Request.URL; len(res.Redirects) > 0 && final.String() != t.u.String() {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		final.Fragment = ""
		if c.internal(&final) {
			c.enqueue(task{u: &final, depth: t.depth, page: true})
		}
		return nil
	}
	ctype, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if resp.StatusCode >= 400 || (ctype != "text/html" && ctype != "application/xhtml+xml") {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		return nil
	}
	page, err := Parse(io.LimitReader(resp.Body, 10<<20), resp.Request.URL)
	if err != nil {
		res.Error = "HTML: " + err.Error()
		return nil
	}
	res.ids = page.IDs
	res.parsed = true
	c.mu.Lock()
	c.pages++
	c.mu.Unlock()
	return page
}

// check verifies a resource or external page: HEAD first, GET when the server does not like HEAD.
func (c *crawler) check(ctx context.Context, u *url.URL, res *Result) {
	resp, err := c.do(ctx, http.MethodHead, u, res)
	if err == nil {
		resp.Body.Close()
		switch resp.StatusCode {
		case http.StatusMethodNotAllowed, http.StatusNotImplemented, http.StatusForbidden, http.StatusNotFound:
		default:
			return
		}
	}
	res.Error, res.Status, res.Redirects = "", 0, nil
	resp, err = c.do(ctx, http.MethodGet, u, res)
	if err == nil {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		resp.Body.Close()
	}
}

func (c *crawler) do(ctx context.Context, method string, u *url.URL, res *Result) (*http.Response, error) {
	c.limiter.wait(ctx, u.Host)
	req, err := http.NewRequestWithContext(ctx, method, u.String(), nil)
	if err != nil {
		res.Error = err.Error()
		return nil, err
	}
	req.Header.Set("User-Agent", c.cfg.UserAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,*/*;q=0.8")
	var hops []string
	client := *c.client
	client.CheckRedirect = func(r *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errors.New("more than 10 redirects")
		}
		hops = append(hops, r.URL.String())
		return nil
	}
	resp, err := client.Do(req)
	res.Redirects = hops
	if err != nil {
		res.Error = simplify(err)
		return nil, err
	}
	res.Status = resp.StatusCode
	return resp, nil
}

func (c *crawler) fetchRobots(ctx context.Context) *Robots {
	u := *c.cfg.Start
	u.Path, u.RawQuery, u.Fragment = "/robots.txt", "", ""
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil
	}
	req.Header.Set("User-Agent", c.cfg.UserAgent)
	resp, err := c.client.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil
	}
	return ParseRobots(io.LimitReader(resp.Body, 512<<10), c.cfg.UserAgent)
}

// simplify turns url.Error chains into short messages for the report.
func simplify(err error) string {
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "no such host"):
		return "domain does not exist"
	case strings.Contains(msg, "connection refused"):
		return "connection refused"
	case strings.Contains(msg, "Client.Timeout") || strings.Contains(msg, "deadline exceeded"):
		return "timeout"
	case strings.Contains(msg, "certificate"):
		return "TLS certificate problem: " + msg
	}
	return msg
}

// limiter spaces out requests to the same host.
type limiter struct {
	gap  time.Duration
	mu   sync.Mutex
	next map[string]time.Time
}

func newLimiter(rps float64) *limiter {
	l := &limiter{next: map[string]time.Time{}}
	if rps > 0 {
		l.gap = time.Duration(float64(time.Second) / rps)
	}
	return l
}

func (l *limiter) wait(ctx context.Context, host string) {
	if l.gap == 0 {
		return
	}
	l.mu.Lock()
	now := time.Now()
	at := l.next[host]
	if at.Before(now) {
		at = now
	}
	l.next[host] = at.Add(l.gap)
	l.mu.Unlock()
	select {
	case <-time.After(time.Until(at)):
	case <-ctx.Done():
	}
}

// String is used in debug output.
func (t task) String() string { return fmt.Sprintf("%s depth=%d page=%v", t.u, t.depth, t.page) }
