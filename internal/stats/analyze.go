package stats

import (
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/officialasishkumar/firstmerge/internal/gh"
)

var botRe = regexp.MustCompile(`(?i)(\[bot\]$|bot$|robot$|-bot-|easycla|codecov|netlify|sonarcloud|sonarqube|mergify|coderabbit|copilot|dependabot|renovate|github-actions|-ci-|-testing$|^cla[-_]|claassistant|prow|gemini-code-assist|deepsource|snyk|stale|allcontributors|welcome|^poiana$|^openshift-ci|^istio-testing$|^knative-prow|^tekton-robot$|^google-cla$|^cncf-ci$|^l5io$)`)

// IsBot reports whether an actor is automation rather than a person.
func IsBot(a *gh.Actor) bool {
	if a == nil {
		return true // deleted accounts ("ghost") are excluded with bots
	}
	return a.Type == "Bot" || botRe.MatchString(a.Login)
}

var insider = map[string]bool{"MEMBER": true, "OWNER": true, "COLLABORATOR": true}

// Bucket is a group of first-timer PRs and how they ended.
type Bucket struct {
	Label  string  `json:"label"`
	PRs    int     `json:"prs"`
	Merged int     `json:"merged"`
	Rate   float64 `json:"mergeRate"`
}

func (b *Bucket) add(merged bool) {
	b.PRs++
	if merged {
		b.Merged++
	}
}

func (b *Bucket) done() {
	if b.PRs > 0 {
		b.Rate = float64(b.Merged) / float64(b.PRs)
	}
}

// Project is one repository's first-timer numbers.
type Project struct {
	Repo           string        `json:"repo"`
	FirstTimers    int           `json:"firstTimerPRs"`
	Merged         int           `json:"merged"`
	Rate           float64       `json:"mergeRate"`
	MedianResponse time.Duration `json:"medianFirstHumanResponse"`
	NoResponse     float64       `json:"noHumanResponseShare"`
}

// Report is everything firstmerge report prints.
type Report struct {
	From               time.Time     `json:"from"`
	To                 time.Time     `json:"to"`
	DataFrom           time.Time     `json:"dataFrom"`
	Repos              int           `json:"repos"`
	PRs                int           `json:"prsAnalysed"`
	BotPRs             int           `json:"botPRsExcluded"`
	FirstTimer         Bucket        `json:"firstTimer"`
	Returning          Bucket        `json:"returningNonMember"`
	Insider            Bucket        `json:"member"`
	FirstClosed        int           `json:"firstTimerClosedUnmerged"`
	FirstOpen          int           `json:"firstTimerStillOpen"`
	MedianResponse     time.Duration `json:"medianFirstHumanResponse"`
	P75Response        time.Duration `json:"p75FirstHumanResponse"`
	Within24h          float64       `json:"respondedWithin24h"`
	Within7d           float64       `json:"respondedWithin7d"`
	NeverResponded     float64       `json:"neverRespondedByHuman"`
	BotOnly            float64       `json:"onlyBotsResponded"`
	MedianMergeTime    time.Duration `json:"medianTimeToMerge"`
	MedianInsiderMerge time.Duration `json:"medianTimeToMergeMembers"`
	ByResponse         []Bucket      `json:"byFirstResponse"`
	BySize             []Bucket      `json:"bySize"`
	ByIssue            []Bucket      `json:"byLinkedIssue"`
	ByBody             []Bucket      `json:"byDescription"`
	ByFiles            []Bucket      `json:"byFilesChanged"`
	Retained           Bucket        `json:"secondPRAfterMerge"`   // merged first PR -> opened another
	RetainedNotMerged  Bucket        `json:"secondPRAfterNoMerge"` // unmerged first PR -> opened another
	Projects           []Project     `json:"projects"`
}

// firstHuman returns when someone other than the author and bots first
// reviewed or commented, and whether only bots responded.
func firstHuman(p gh.PR) (time.Time, bool) {
	var first time.Time
	botSeen := false
	for _, e := range p.Events {
		if e.At.IsZero() {
			continue
		}
		if IsBot(e.Author) {
			botSeen = true
			continue
		}
		if p.Author != nil && e.Author.Login == p.Author.Login {
			continue
		}
		if first.IsZero() || e.At.Before(first) {
			first = e.At
		}
	}
	return first, first.IsZero() && botSeen
}

// Analyze computes the report. A first-timer PR is the earliest PR an author
// opened in a repository within the collected data, from someone who is not
// a member, owner or collaborator, and opened at least 60 days after the
// data begins so that earlier contributions are visible. Zero from/to pick a
// one-year cohort ending six months before the newest PR, so that outcomes
// have had time to settle.
func Analyze(prs []gh.PR, from, to time.Time) *Report {
	r := &Report{}
	var newest time.Time
	r.DataFrom = time.Now()
	for _, p := range prs {
		if p.CreatedAt.After(newest) {
			newest = p.CreatedAt
		}
		if p.CreatedAt.Before(r.DataFrom) {
			r.DataFrom = p.CreatedAt
		}
	}
	if to.IsZero() {
		to = newest.AddDate(0, -6, 0)
	}
	if from.IsZero() {
		from = to.AddDate(-1, 0, 0)
	}
	if min := r.DataFrom.AddDate(0, 0, 60); from.Before(min) {
		from = min
	}
	r.From, r.To = from, to

	sort.Slice(prs, func(i, j int) bool { return prs[i].CreatedAt.Before(prs[j].CreatedAt) })
	type key struct{ repo, login string }
	firstPR := map[key]int{} // index of the author's first PR per repo
	count := map[key]int{}
	repos := map[string]bool{}
	for i, p := range prs {
		repos[p.Repo] = true
		if IsBot(p.Author) {
			continue
		}
		k := key{p.Repo, p.Author.Login}
		if _, ok := firstPR[k]; !ok {
			firstPR[k] = i
		}
		count[k]++
	}
	r.Repos = len(repos)

	resp := []string{"first human response within 24 hours", "1 to 7 days", "more than 7 days", "no human response"}
	size := []string{"under 10 lines", "10 to 99 lines", "100 to 499 lines", "500 lines or more"}
	byResp := make([]Bucket, 4)
	bySize := make([]Bucket, 4)
	byIssue := []Bucket{{Label: "links an issue it closes"}, {Label: "no linked issue"}}
	byBody := []Bucket{{Label: "description under 100 characters"}, {Label: "description of 100+ characters"}}
	byFiles := []Bucket{{Label: "1 file"}, {Label: "2 to 5 files"}, {Label: "6 or more files"}}
	for i := range resp {
		byResp[i].Label, bySize[i].Label = resp[i], size[i]
	}
	var waits, merges, insiderMerges []time.Duration
	within24, within7, never, botOnly := 0, 0, 0, 0
	proj := map[string]*Project{}
	projWaits := map[string][]time.Duration{}
	projNever := map[string]int{}

	for i, p := range prs {
		if p.CreatedAt.Before(from) || !p.CreatedAt.Before(to) {
			continue
		}
		if IsBot(p.Author) {
			r.BotPRs++
			continue
		}
		r.PRs++
		merged := p.MergedAt != nil
		k := key{p.Repo, p.Author.Login}
		switch {
		case insider[p.Association]:
			r.Insider.add(merged)
			if merged {
				insiderMerges = append(insiderMerges, p.MergedAt.Sub(p.CreatedAt))
			}
			continue
		case firstPR[k] != i:
			r.Returning.add(merged)
			continue
		}
		// first-timer
		r.FirstTimer.add(merged)
		if !merged {
			if p.State == "OPEN" {
				r.FirstOpen++
			} else {
				r.FirstClosed++
			}
		} else {
			merges = append(merges, p.MergedAt.Sub(p.CreatedAt))
		}
		pr := proj[p.Repo]
		if pr == nil {
			pr = &Project{Repo: p.Repo}
			proj[p.Repo] = pr
		}
		pr.FirstTimers++
		if merged {
			pr.Merged++
		}
		at, onlyBots := firstHuman(p)
		switch {
		case at.IsZero():
			never++
			projNever[p.Repo]++
			byResp[3].add(merged)
			if onlyBots {
				botOnly++
			}
		default:
			w := at.Sub(p.CreatedAt)
			if w < 0 {
				w = 0
			}
			waits = append(waits, w)
			projWaits[p.Repo] = append(projWaits[p.Repo], w)
			switch {
			case w <= 24*time.Hour:
				within24++
				within7++
				byResp[0].add(merged)
			case w <= 7*24*time.Hour:
				within7++
				byResp[1].add(merged)
			default:
				byResp[2].add(merged)
			}
		}
		lines := p.Additions + p.Deletions
		switch {
		case lines < 10:
			bySize[0].add(merged)
		case lines < 100:
			bySize[1].add(merged)
		case lines < 500:
			bySize[2].add(merged)
		default:
			bySize[3].add(merged)
		}
		if p.LinkedIssues > 0 {
			byIssue[0].add(merged)
		} else {
			byIssue[1].add(merged)
		}
		if p.BodyLen < 100 {
			byBody[0].add(merged)
		} else {
			byBody[1].add(merged)
		}
		switch {
		case p.ChangedFiles <= 1:
			byFiles[0].add(merged)
		case p.ChangedFiles <= 5:
			byFiles[1].add(merged)
		default:
			byFiles[2].add(merged)
		}
		// Did they come back? Only count authors whose first PR leaves at
		// least 90 days of data after it.
		if p.CreatedAt.Before(newest.AddDate(0, 0, -90)) {
			again := count[k] > 1
			if merged {
				r.Retained.add(again)
			} else if p.State != "OPEN" {
				r.RetainedNotMerged.add(again)
			}
		}
	}
	n := r.FirstTimer.PRs
	if n > 0 {
		r.Within24h = float64(within24) / float64(n)
		r.Within7d = float64(within7) / float64(n)
		r.NeverResponded = float64(never) / float64(n)
		r.BotOnly = float64(botOnly) / float64(n)
	}
	r.MedianResponse, r.P75Response = quantile(waits, 0.5), quantile(waits, 0.75)
	r.MedianMergeTime = quantile(merges, 0.5)
	r.MedianInsiderMerge = quantile(insiderMerges, 0.5)
	for _, b := range []*Bucket{&r.FirstTimer, &r.Returning, &r.Insider, &r.Retained, &r.RetainedNotMerged} {
		b.done()
	}
	for _, bs := range [][]Bucket{byResp, bySize, byIssue, byBody, byFiles} {
		for i := range bs {
			bs[i].done()
		}
	}
	r.ByResponse, r.BySize, r.ByIssue, r.ByBody, r.ByFiles = byResp, bySize, byIssue, byBody, byFiles
	for repo, p := range proj {
		if p.FirstTimers > 0 {
			p.Rate = float64(p.Merged) / float64(p.FirstTimers)
			p.NoResponse = float64(projNever[repo]) / float64(p.FirstTimers)
		}
		p.MedianResponse = quantile(projWaits[repo], 0.5)
		r.Projects = append(r.Projects, *p)
	}
	sort.Slice(r.Projects, func(i, j int) bool { return r.Projects[i].FirstTimers > r.Projects[j].FirstTimers })
	return r
}

func quantile(ds []time.Duration, q float64) time.Duration {
	if len(ds) == 0 {
		return 0
	}
	s := append([]time.Duration(nil), ds...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	return s[int(q*float64(len(s)-1))]
}

// Dur prints a duration the way people say it.
func Dur(d time.Duration) string {
	switch {
	case d == 0:
		return "-"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%.1fh", d.Hours())
	default:
		return fmt.Sprintf("%.1fd", d.Hours()/24)
	}
}

func pct(f float64) string { return fmt.Sprintf("%.0f%%", f*100) }

// Markdown writes the report.
func Markdown(w io.Writer, r *Report) {
	fmt.Fprintf(w, "# First-time contributors' pull requests\n\n")
	fmt.Fprintf(w, "%d repositories; PRs opened %s to %s (data from %s). %d human PRs analysed, %d bot PRs excluded.\n\n",
		r.Repos, r.From.Format("2 Jan 2006"), r.To.Format("2 Jan 2006"), r.DataFrom.Format("2 Jan 2006"), r.PRs, r.BotPRs)
	fmt.Fprintf(w, "| who opened it | PRs | merged |\n|---|---:|---:|\n")
	fmt.Fprintf(w, "| first-time contributor | %d | %s |\n", r.FirstTimer.PRs, pct(r.FirstTimer.Rate))
	fmt.Fprintf(w, "| returning contributor (no commit access) | %d | %s |\n", r.Returning.PRs, pct(r.Returning.Rate))
	fmt.Fprintf(w, "| member / collaborator | %d | %s |\n\n", r.Insider.PRs, pct(r.Insider.Rate))
	fmt.Fprintf(w, "Of first-timer PRs: %d merged, %d closed without merging, %d still open.\n\n", r.FirstTimer.Merged, r.FirstClosed, r.FirstOpen)
	fmt.Fprintf(w, "## Waiting for a person\n\n")
	fmt.Fprintf(w, "- Median wait for the first human review or comment: **%s** (75th percentile %s)\n", Dur(r.MedianResponse), Dur(r.P75Response))
	fmt.Fprintf(w, "- Answered within 24 hours: **%s**; within a week: %s\n", pct(r.Within24h), pct(r.Within7d))
	fmt.Fprintf(w, "- Never answered by a person: **%s** (only bots answered: %s)\n", pct(r.NeverResponded), pct(r.BotOnly))
	fmt.Fprintf(w, "- Median time to merge: first-timers %s, members %s\n\n", Dur(r.MedianMergeTime), Dur(r.MedianInsiderMerge))
	table := func(title string, bs []Bucket) {
		fmt.Fprintf(w, "## %s\n\n| | PRs | merged |\n|---|---:|---:|\n", title)
		for _, b := range bs {
			fmt.Fprintf(w, "| %s | %d | %s |\n", b.Label, b.PRs, pct(b.Rate))
		}
		fmt.Fprintln(w)
	}
	table("Merge rate by how fast a person first responded", r.ByResponse)
	table("Merge rate by size (lines added + deleted)", r.BySize)
	table("Merge rate by files changed", r.ByFiles)
	table("Merge rate by linked issue", r.ByIssue)
	fmt.Fprintf(w, "## Did they come back?\n\n")
	fmt.Fprintf(w, "- First PR merged: **%s** opened another PR in the same repository (%d people)\n", pct(r.Retained.Rate), r.Retained.PRs)
	fmt.Fprintf(w, "- First PR closed without merging: **%s** did (%d people)\n\n", pct(r.RetainedNotMerged.Rate), r.RetainedNotMerged.PRs)
	fmt.Fprintf(w, "## By project\n\n| repository | first-timer PRs | merged | median first human response | never answered |\n|---|---:|---:|---:|---:|\n")
	for _, p := range r.Projects {
		if p.FirstTimers < 10 {
			continue
		}
		fmt.Fprintf(w, "| %s | %d | %s | %s | %s |\n", p.Repo, p.FirstTimers, pct(p.Rate), Dur(p.MedianResponse), pct(p.NoResponse))
	}
	fmt.Fprintf(w, "\nProjects with fewer than 10 first-timer PRs are omitted from this table but included above.\n")
	_ = strings.TrimSpace
}
