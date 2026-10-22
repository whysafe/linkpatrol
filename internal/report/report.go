// Package report prints crawl findings as text, JSON or Markdown.
package report

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/whysafe/linkpatrol/internal/crawl"
)

// Summary is the machine-readable result.
type Summary struct {
	Start    string          `json:"start"`
	Pages    int             `json:"pages"`
	Checked  int             `json:"checked"`
	Errors   int             `json:"errors"`
	Warnings int             `json:"warnings"`
	Seconds  float64         `json:"seconds"`
	Problems []crawl.Problem `json:"problems"`
}

// Summarize filters problems by the minimum severity and counts them.
func Summarize(r *crawl.Report, warnings bool) Summary {
	s := Summary{Start: r.Start, Pages: r.Pages, Checked: r.Checked, Seconds: r.Duration.Round(10 * time.Millisecond).Seconds()}
	s.Problems = []crawl.Problem{}
	for _, p := range r.Problems() {
		if p.Severity == crawl.Warning {
			s.Warnings++
			if !warnings {
				continue
			}
		} else {
			s.Errors++
		}
		s.Problems = append(s.Problems, p)
	}
	return s
}

// JSON writes the summary as indented JSON.
func JSON(w io.Writer, s Summary) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(s)
}

var kindTitle = map[string]string{
	"broken":   "broken",
	"anchor":   "missing anchor",
	"redirect": "redirect",
	"mixed":    "mixed content",
	"robots":   "skipped",
}

// Text writes a human-readable report grouped by page.
func Text(w io.Writer, s Summary, color bool) {
	paint := func(code, text string) string {
		if !color {
			return text
		}
		return "\x1b[" + code + "m" + text + "\x1b[0m"
	}
	page := ""
	for _, p := range s.Problems {
		if p.Page != page {
			page = p.Page
			fmt.Fprintf(w, "\n%s\n", paint("1", page))
		}
		mark := paint("31", "✗")
		if p.Severity == crawl.Warning {
			mark = paint("33", "!")
		}
		label := p.Link
		if p.Text != "" {
			label += paint("2", fmt.Sprintf(" (%s)", p.Text))
		}
		fmt.Fprintf(w, "  %s %-14s %s\n      %s\n", mark, kindTitle[p.Kind], label, paint("2", p.Detail))
	}
	status := paint("32", "no problems found")
	if s.Errors > 0 {
		status = paint("31", fmt.Sprintf("%d error(s)", s.Errors))
	}
	fmt.Fprintf(w, "\n%s, %d warning(s) · %d pages crawled, %d URLs checked in %.1fs\n",
		status, s.Warnings, s.Pages, s.Checked, s.Seconds)
}

// Markdown writes a table suitable for a GitHub Actions job summary or an issue.
func Markdown(w io.Writer, s Summary) {
	fmt.Fprintf(w, "## Link check: %s\n\n", s.Start)
	fmt.Fprintf(w, "**%d error(s)**, %d warning(s) · %d pages, %d URLs, %.1fs\n\n", s.Errors, s.Warnings, s.Pages, s.Checked, s.Seconds)
	if len(s.Problems) == 0 {
		fmt.Fprintln(w, "No problems found. :white_check_mark:")
		return
	}
	fmt.Fprintln(w, "| | Page | Link | Problem |")
	fmt.Fprintln(w, "|---|---|---|---|")
	esc := strings.NewReplacer("|", "\\|", "\n", " ")
	for _, p := range s.Problems {
		icon := ":x:"
		if p.Severity == crawl.Warning {
			icon = ":warning:"
		}
		fmt.Fprintf(w, "| %s | %s | `%s` | %s: %s |\n", icon, esc.Replace(p.Page), esc.Replace(p.Link),
			kindTitle[p.Kind], esc.Replace(p.Detail))
	}
}
