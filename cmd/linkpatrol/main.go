// Command linkpatrol crawls a website and reports broken links, missing
// anchors, redirects and mixed content. Exit code 1 means errors were found.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/whysafe/linkpatrol/internal/crawl"
	"github.com/whysafe/linkpatrol/internal/report"
)

var version = "dev"

const usage = `linkpatrol: find broken links on a website

Usage:
  linkpatrol [flags] https://example.com

Flags:
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("linkpatrol", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprint(stderr, usage)
		fs.PrintDefaults()
	}
	workers := fs.Int("c", 8, "parallel requests")
	depth := fs.Int("depth", -1, "max clicks from the start page to crawl (-1 = whole site)")
	maxPages := fs.Int("max-pages", 5000, "stop after this many internal pages")
	timeout := fs.Duration("timeout", 15*time.Second, "timeout per request")
	external := fs.Bool("external", true, "also check links to other sites")
	rps := fs.Float64("rps", 0, "max requests per second per host (0 = unlimited)")
	format := fs.String("format", "text", "output: text, json or markdown")
	warnings := fs.Bool("warnings", true, "show warnings (redirects, robots.txt skips)")
	strict := fs.Bool("strict", false, "exit with code 1 on warnings too")
	ignoreRobots := fs.Bool("ignore-robots", false, "crawl pages disallowed by robots.txt")
	showVersion := fs.Bool("version", false, "print version")

	// Flags may come after the URL: linkpatrol https://site -c 4
	var flags, pos []string
	for i := 0; i < len(args); i++ {
		if strings.HasPrefix(args[i], "-") {
			flags = append(flags, args[i])
			if !strings.Contains(args[i], "=") && i+1 < len(args) && !isBool(fs, args[i]) {
				i++
				flags = append(flags, args[i])
			}
		} else {
			pos = append(pos, args[i])
		}
	}
	if err := fs.Parse(flags); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if *showVersion {
		fmt.Fprintln(stdout, "linkpatrol", version)
		return 0
	}
	if len(pos) != 1 {
		fs.Usage()
		return 2
	}
	raw := pos[0]
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	start, err := url.Parse(raw)
	if err != nil || start.Host == "" {
		fmt.Fprintf(stderr, "error: %q is not a URL\n", pos[0])
		return 2
	}
	if start.Path == "" {
		start.Path = "/"
	}
	if *format != "text" && *format != "json" && *format != "markdown" {
		fmt.Fprintf(stderr, "error: unknown format %q\n", *format)
		return 2
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	rep, err := crawl.Run(ctx, crawl.Config{
		Start: start, Workers: *workers, MaxDepth: *depth, MaxPages: *maxPages, Timeout: *timeout,
		External: *external, RPS: *rps, IgnoreRobots: *ignoreRobots,
	})
	if err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintln(stderr, "error:", err)
		return 2
	}
	if res := rep.Results[start.String()]; res != nil && res.Error != "" && rep.Pages <= 1 {
		fmt.Fprintf(stderr, "error: cannot open %s: %s\n", start, res.Error)
		return 2
	}

	s := report.Summarize(rep, *warnings)
	switch *format {
	case "json":
		report.JSON(stdout, s)
	case "markdown":
		report.Markdown(stdout, s)
	default:
		report.Text(stdout, s, isTerminal(stdout))
	}
	if s.Errors > 0 || (*strict && s.Warnings > 0) {
		return 1
	}
	return 0
}

func isBool(fs *flag.FlagSet, arg string) bool {
	f := fs.Lookup(strings.TrimLeft(arg, "-"))
	if f == nil {
		return false
	}
	b, ok := f.Value.(interface{ IsBoolFlag() bool })
	return ok && b.IsBoolFlag()
}

func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok || os.Getenv("NO_COLOR") != "" {
		return false
	}
	info, err := f.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}
