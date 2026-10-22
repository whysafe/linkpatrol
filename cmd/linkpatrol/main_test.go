package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func server(t *testing.T, broken bool) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			w.Header().Set("Content-Type", "text/html")
			link := "/ok"
			if broken {
				link = "/gone"
			}
			fmt.Fprintf(w, `<a href="%s">link</a><a href="/moved">old</a>`, link)
		case "/ok":
			fmt.Fprint(w, "fine")
		case "/moved":
			http.Redirect(w, r, "/ok", http.StatusFound)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func cli(args ...string) (int, string, string) {
	var out, errb bytes.Buffer
	code := run(args, &out, &errb)
	return code, out.String(), errb.String()
}

func TestExitCodeReflectsErrors(t *testing.T) {
	if code, out, _ := cli(server(t, false)); code != 0 || !strings.Contains(out, "no problems found") {
		t.Errorf("clean site: code=%d out=%q", code, out)
	}
	if code, out, _ := cli(server(t, true)); code != 1 || !strings.Contains(out, "/gone") {
		t.Errorf("broken site: code=%d out=%q", code, out)
	}
}

func TestStrictFailsOnWarnings(t *testing.T) {
	if code, _, _ := cli(server(t, false), "-strict"); code != 1 {
		t.Errorf("redirect warning with -strict: code=%d, want 1", code)
	}
}

func TestJSONOutputAndFlagsAfterURL(t *testing.T) {
	code, out, _ := cli(server(t, true), "-format", "json", "-c", "2")
	if code != 1 {
		t.Fatalf("code = %d", code)
	}
	var s struct {
		Errors   int `json:"errors"`
		Warnings int `json:"warnings"`
		Problems []struct {
			Link, Kind string
		} `json:"problems"`
	}
	if err := json.Unmarshal([]byte(out), &s); err != nil {
		t.Fatalf("bad JSON: %v\n%s", err, out)
	}
	if s.Errors != 1 || s.Warnings != 1 || len(s.Problems) != 2 {
		t.Errorf("summary = %+v", s)
	}
}

func TestMarkdownAndHiddenWarnings(t *testing.T) {
	_, out, _ := cli(server(t, true), "-format=markdown", "-warnings=false")
	if !strings.Contains(out, "| :x: |") || strings.Contains(out, ":warning:") {
		t.Errorf("markdown:\n%s", out)
	}
}

func TestUsageErrors(t *testing.T) {
	for _, args := range [][]string{{}, {"a", "b"}, {"https://x.test", "-format", "xml"}} {
		if code, _, _ := cli(args...); code != 2 {
			t.Errorf("%v: code=%d, want 2", args, code)
		}
	}
	if code, _, errb := cli("http://127.0.0.1:1"); code != 2 || !strings.Contains(errb, "cannot open") {
		t.Errorf("unreachable start: code=%d err=%q", code, errb)
	}
}
