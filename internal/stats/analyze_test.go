package stats

import (
	"testing"
	"time"

	"github.com/officialasishkumar/firstmerge/internal/gh"
)

func at(days float64) time.Time {
	return time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(days * 24 * float64(time.Hour)))
}

func pr(repo, login, assoc string, created float64, merged bool, events ...gh.Event) gh.PR {
	p := gh.PR{Repo: repo, Author: &gh.Actor{Login: login, Type: "User"}, Association: assoc, CreatedAt: at(created), State: "CLOSED", Events: events}
	if merged {
		m := at(created + 2)
		p.MergedAt, p.State = &m, "MERGED"
	}
	return p
}

func ev(login string, day float64) gh.Event {
	return gh.Event{Author: &gh.Actor{Login: login, Type: "User"}, At: at(day), Kind: "comment"}
}

func TestFirstTimersAndResponses(t *testing.T) {
	prs := []gh.PR{
		pr("o/r", "early", "CONTRIBUTOR", 1, true),                                                                            // sets data start; outside cohort
		pr("o/r", "alice", "CONTRIBUTOR", 100, true, ev("maint", 100.5)),                                                      // first-timer, answered in 12h, merged
		pr("o/r", "alice", "CONTRIBUTOR", 120, true),                                                                          // returning
		pr("o/r", "bob", "NONE", 110, false, gh.Event{Author: &gh.Actor{Login: "k8s-ci-robot", Type: "User"}, At: at(110.1)}), // bot only
		pr("o/r", "carol", "FIRST_TIME_CONTRIBUTOR", 130, false, ev("carol", 131), ev("maint", 140)),                          // own comment ignored; 10d wait
		pr("o/r", "dev", "MEMBER", 105, true),
		{Repo: "o/r", Author: &gh.Actor{Login: "dependabot", Type: "Bot"}, CreatedAt: at(115)},
		pr("o/r", "late", "NONE", 600, false), // newest PR, outside cohort
	}
	r := Analyze(prs, at(61), at(400))
	if r.FirstTimer.PRs != 3 || r.FirstTimer.Merged != 1 {
		t.Fatalf("first-timers = %+v", r.FirstTimer)
	}
	if r.Returning.PRs != 1 || r.Insider.PRs != 1 || r.BotPRs != 1 {
		t.Errorf("returning %+v insider %+v bots %d", r.Returning, r.Insider, r.BotPRs)
	}
	if r.ByResponse[0].PRs != 1 || r.ByResponse[2].PRs != 1 || r.ByResponse[3].PRs != 1 {
		t.Errorf("by response = %+v", r.ByResponse)
	}
	if r.BotOnly == 0 {
		t.Error("bob's PR was answered only by a bot")
	}
	if r.Retained.PRs != 1 || r.Retained.Merged != 1 {
		t.Errorf("alice came back after a merge: %+v", r.Retained)
	}
}

func TestIsBot(t *testing.T) {
	for _, l := range []string{"k8s-ci-robot", "dependabot[bot]", "linux-foundation-easycla", "codecov-commenter", "netlify"} {
		if !IsBot(&gh.Actor{Login: l, Type: "User"}) {
			t.Errorf("%s should be a bot", l)
		}
	}
	for _, l := range []string{"alice", "robert", "bottlerocket-dev"} {
		if IsBot(&gh.Actor{Login: l, Type: "User"}) {
			t.Errorf("%s is a person", l)
		}
	}
}
