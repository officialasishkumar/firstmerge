// firstmerge measures what happens to first-time contributors' pull requests
// in a set of GitHub repositories: how many are merged, how long they wait
// for a human, and which things about a PR go with it being merged.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"time"

	"github.com/officialasishkumar/firstmerge/internal/gh"
	"github.com/officialasishkumar/firstmerge/internal/stats"
)

var version = "dev"

const usage = `firstmerge: what happens to a first-time contributor's pull request?

Usage:
  firstmerge collect -repos FILE [-since 2024-05-01] [-out prs.jsonl]
      Fetch every PR opened since the date in each owner/name listed in FILE.
      Needs GITHUB_TOKEN (or GH_TOKEN). Resumable: repos already in -out are skipped.
  firstmerge report [-from 2025-05-01 -to 2026-05-01] [-json] prs.jsonl
      Who counts as a first-timer, merge rates, wait for a first human
      response, and what goes with a merge.
  firstmerge version
`

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "firstmerge:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		fmt.Print(usage)
		return nil
	}
	switch args[0] {
	case "collect":
		return collect(args[1:])
	case "report":
		return report(args[1:])
	case "version":
		fmt.Println("firstmerge", version)
		return nil
	}
	return fmt.Errorf("unknown command %q", args[0])
}

func collect(args []string) error {
	fs := flag.NewFlagSet("collect", flag.ContinueOnError)
	reposFile := fs.String("repos", "", "file with one owner/name per line")
	sinceS := fs.String("since", "2024-05-01", "first creation date to fetch")
	out := fs.String("out", "prs.jsonl", "output JSON Lines file (appended)")
	workers := fs.Int("workers", 4, "repositories fetched in parallel")
	if err := fs.Parse(args); err != nil {
		return err
	}
	token := os.Getenv("GITHUB_TOKEN")
	if token == "" {
		token = os.Getenv("GH_TOKEN")
	}
	if token == "" || *reposFile == "" {
		return errors.New("need -repos FILE and GITHUB_TOKEN")
	}
	since, err := time.Parse("2006-01-02", *sinceS)
	if err != nil {
		return err
	}
	repos, err := readLines(*reposFile)
	if err != nil {
		return err
	}
	done := map[string]bool{}
	if prs, err := stats.Load(*out); err == nil {
		for _, p := range prs {
			done[p.Repo] = true
		}
	}
	if err := os.MkdirAll(*out+".parts", 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(*out, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	c := &gh.Client{Token: token, HTTP: &http.Client{Timeout: 90 * time.Second}, Log: os.Stderr}
	var mu sync.Mutex
	jobs := make(chan int)
	var wg sync.WaitGroup
	for w := 0; w < *workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				repo := repos[i]
				start := time.Now()
				// Buffer a repo and write it whole, so an interrupted run never
				// leaves half a repo that resume would then skip.
				// Stream to a per-repo part file; rename it when the repo is
				// complete so an interrupted run never counts half a repo.
				part := *out + ".parts/" + strings.ReplaceAll(repo, "/", "__") + ".jsonl"
				pf, perr := os.Create(part + ".tmp")
				if perr != nil {
					fmt.Fprintln(os.Stderr, perr)
					continue
				}
				penc := json.NewEncoder(pf)
				n, err := c.Repo(ctx, repo, since, func(p gh.PR) error { return penc.Encode(p) })
				pf.Close()
				mu.Lock()
				if err != nil {
					fmt.Fprintf(os.Stderr, "[%d/%d] %s: %v (skipped)\n", i+1, len(repos), repo, err)
					os.Remove(part + ".tmp")
				} else {
					b, _ := os.ReadFile(part + ".tmp")
					_, _ = f.Write(b)
					os.Remove(part + ".tmp")
					fmt.Fprintf(os.Stderr, "[%d/%d] %s: %d PRs in %s\n", i+1, len(repos), repo, n, time.Since(start).Round(time.Second))
				}
				mu.Unlock()
			}
		}()
	}
	for i, repo := range repos {
		if done[repo] {
			continue
		}
		select {
		case jobs <- i:
		case <-ctx.Done():
		}
		if ctx.Err() != nil {
			break
		}
	}
	close(jobs)
	wg.Wait()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return nil
}

func report(args []string) error {
	fs := flag.NewFlagSet("report", flag.ContinueOnError)
	fromS := fs.String("from", "", "cohort start (default: one year before -to)")
	toS := fs.String("to", "", "cohort end (default: six months before the newest PR, so outcomes have settled)")
	asJSON := fs.Bool("json", false, "print JSON instead of Markdown")
	skip := fs.String("skip", "", "comma-separated repositories to leave out (e.g. ones that merge outside GitHub)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: firstmerge report [flags] prs.jsonl")
	}
	all, err := stats.Load(fs.Arg(0))
	if err != nil {
		return err
	}
	drop := map[string]bool{}
	for _, r := range strings.Split(*skip, ",") {
		drop[strings.TrimSpace(r)] = true
	}
	var prs []gh.PR
	for _, p := range all {
		if !drop[p.Repo] {
			prs = append(prs, p)
		}
	}
	var from, to time.Time
	if *toS != "" {
		if to, err = time.Parse("2006-01-02", *toS); err != nil {
			return err
		}
	}
	if *fromS != "" {
		if from, err = time.Parse("2006-01-02", *fromS); err != nil {
			return err
		}
	}
	r := stats.Analyze(prs, from, to)
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(r)
	}
	stats.Markdown(os.Stdout, r)
	return nil
}

func readLines(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []string
	s := bufio.NewScanner(f)
	for s.Scan() {
		l := strings.TrimSpace(s.Text())
		if l != "" && !strings.HasPrefix(l, "#") {
			out = append(out, l)
		}
	}
	return out, s.Err()
}
