// Package gh collects pull requests from the GitHub GraphQL API.
package gh

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Actor is the author of a PR, review or comment.
type Actor struct {
	Login string `json:"login"`
	Type  string `json:"__typename"`
}

// Event is a review or comment: who responded, and when.
type Event struct {
	Author *Actor    `json:"author"`
	At     time.Time `json:"at"`
	Kind   string    `json:"kind"` // review, comment
	State  string    `json:"state,omitempty"`
}

// PR is one pull request, flattened to what the report needs.
type PR struct {
	Repo         string     `json:"repo"`
	Number       int        `json:"number"`
	Author       *Actor     `json:"author"`
	Association  string     `json:"association"`
	CreatedAt    time.Time  `json:"createdAt"`
	MergedAt     *time.Time `json:"mergedAt,omitempty"`
	ClosedAt     *time.Time `json:"closedAt,omitempty"`
	State        string     `json:"state"`
	Draft        bool       `json:"draft"`
	Additions    int        `json:"additions"`
	Deletions    int        `json:"deletions"`
	ChangedFiles int        `json:"changedFiles"`
	Labels       []string   `json:"labels,omitempty"`
	LinkedIssues int        `json:"linkedIssues"`
	BodyLen      int        `json:"bodyLen"`
	Events       []Event    `json:"events,omitempty"`
}

const query = `query($owner:String!,$name:String!,$after:String,$first:Int!){
  rateLimit{remaining resetAt cost}
  repository(owner:$owner,name:$name){
    pullRequests(first:$first,after:$after,orderBy:{field:CREATED_AT,direction:DESC}){
      pageInfo{hasNextPage endCursor}
      nodes{
        number createdAt mergedAt closedAt state isDraft additions deletions changedFiles authorAssociation
        author{login __typename}
        labels(first:15){nodes{name}}
        closingIssuesReferences{totalCount}
        reviews(first:6){nodes{author{login __typename} submittedAt state}}
        comments(first:6){nodes{author{login __typename} createdAt}}
      }
    }
  }
}`

type page struct {
	Data struct {
		RateLimit struct {
			Remaining int       `json:"remaining"`
			ResetAt   time.Time `json:"resetAt"`
			Cost      int       `json:"cost"`
		} `json:"rateLimit"`
		Repository *struct {
			PullRequests struct {
				PageInfo struct {
					HasNextPage bool   `json:"hasNextPage"`
					EndCursor   string `json:"endCursor"`
				} `json:"pageInfo"`
				Nodes []struct {
					Number            int        `json:"number"`
					CreatedAt         time.Time  `json:"createdAt"`
					MergedAt          *time.Time `json:"mergedAt"`
					ClosedAt          *time.Time `json:"closedAt"`
					State             string     `json:"state"`
					IsDraft           bool       `json:"isDraft"`
					Additions         int        `json:"additions"`
					Deletions         int        `json:"deletions"`
					ChangedFiles      int        `json:"changedFiles"`
					AuthorAssociation string     `json:"authorAssociation"`
					BodyText          string     `json:"bodyText"`
					Author            *Actor     `json:"author"`
					Labels            struct {
						Nodes []struct{ Name string } `json:"nodes"`
					} `json:"labels"`
					ClosingIssuesReferences struct {
						TotalCount int `json:"totalCount"`
					} `json:"closingIssuesReferences"`
					Reviews struct {
						Nodes []struct {
							Author      *Actor    `json:"author"`
							SubmittedAt time.Time `json:"submittedAt"`
							State       string    `json:"state"`
						} `json:"nodes"`
					} `json:"reviews"`
					Comments struct {
						Nodes []struct {
							Author    *Actor    `json:"author"`
							CreatedAt time.Time `json:"createdAt"`
						} `json:"nodes"`
					} `json:"comments"`
				} `json:"nodes"`
			} `json:"pullRequests"`
		} `json:"repository"`
	} `json:"data"`
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors"`
}

// Client talks to api.github.com/graphql with a token.
type Client struct {
	Token string
	HTTP  *http.Client
	Log   io.Writer
}

func (c *Client) do(ctx context.Context, vars map[string]any) (*page, error) {
	body, _ := json.Marshal(map[string]any{"query": query, "variables": vars})
	var lastErr error
	for attempt := 0; attempt < 6; attempt++ {
		req, _ := http.NewRequestWithContext(ctx, "POST", "https://api.github.com/graphql", bytes.NewReader(body))
		req.Header.Set("Authorization", "bearer "+c.Token)
		req.Header.Set("Content-Type", "application/json")
		resp, err := c.HTTP.Do(req)
		if err != nil {
			lastErr = err
		} else {
			b, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode == 200 {
				var p page
				if err := json.Unmarshal(b, &p); err != nil {
					// truncated or HTML error bodies happen under load: retry
					lastErr = err
					sleep(ctx, time.Duration(attempt+1)*10*time.Second)
					continue
				}
				if len(p.Errors) > 0 && p.Data.Repository == nil {
					return nil, fmt.Errorf("graphql: %s", p.Errors[0].Message)
				}
				return &p, nil
			}
			lastErr = fmt.Errorf("HTTP %d: %.200s", resp.StatusCode, b)
			if resp.StatusCode == 502 || resp.StatusCode == 504 {
				// GitHub timed out building the page: ask for fewer PRs.
				if f, ok := vars["first"].(int); ok && f > 5 {
					vars["first"] = f / 2
					body, _ = json.Marshal(map[string]any{"query": query, "variables": vars})
					fmt.Fprintf(c.Log, "  timeout, retrying with %d PRs per page\n", f/2)
					continue
				}
			}
			if resp.StatusCode == 403 || resp.StatusCode == 429 {
				fmt.Fprintf(c.Log, "  rate limited, sleeping 60s\n")
				sleep(ctx, 60*time.Second)
				continue
			}
		}
		sleep(ctx, time.Duration(attempt+1)*5*time.Second)
	}
	return nil, lastErr
}

func sleep(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}

// Repo pages through a repository's PRs, newest first, until createdAt < since.
func (c *Client) Repo(ctx context.Context, repo string, since time.Time, emit func(PR) error) (int, error) {
	owner, name, ok := strings.Cut(repo, "/")
	if !ok {
		return 0, fmt.Errorf("bad repo %q", repo)
	}
	var after any
	n, pages := 0, 0
	for {
		p, err := c.do(ctx, map[string]any{"owner": owner, "name": name, "after": after, "first": 50})
		if err != nil {
			return n, err
		}
		if p.Data.Repository == nil {
			return n, fmt.Errorf("%s: not found", repo)
		}
		prs := p.Data.Repository.PullRequests
		done := false
		for _, x := range prs.Nodes {
			if x.CreatedAt.Before(since) {
				done = true
				break
			}
			pr := PR{Repo: repo, Number: x.Number, Author: x.Author, Association: x.AuthorAssociation,
				CreatedAt: x.CreatedAt, MergedAt: x.MergedAt, ClosedAt: x.ClosedAt, State: x.State,
				Draft: x.IsDraft, Additions: x.Additions, Deletions: x.Deletions, ChangedFiles: x.ChangedFiles,
				LinkedIssues: x.ClosingIssuesReferences.TotalCount, BodyLen: len(x.BodyText)}
			for _, l := range x.Labels.Nodes {
				pr.Labels = append(pr.Labels, l.Name)
			}
			for _, r := range x.Reviews.Nodes {
				pr.Events = append(pr.Events, Event{Author: r.Author, At: r.SubmittedAt, Kind: "review", State: r.State})
			}
			for _, cm := range x.Comments.Nodes {
				pr.Events = append(pr.Events, Event{Author: cm.Author, At: cm.CreatedAt, Kind: "comment"})
			}
			if err := emit(pr); err != nil {
				return n, err
			}
			n++
		}
		pages++
		if pages%20 == 0 {
			fmt.Fprintf(c.Log, "  %s: %d PRs so far (back to %s)\n", repo, n, prs.Nodes[len(prs.Nodes)-1].CreatedAt.Format("2006-01-02"))
		}
		rl := p.Data.RateLimit
		if rl.Remaining < 50 {
			wait := time.Until(rl.ResetAt) + 5*time.Second
			fmt.Fprintf(c.Log, "  rate limit low (%d), sleeping %s\n", rl.Remaining, wait.Round(time.Second))
			sleep(ctx, wait)
		}
		if done || !prs.PageInfo.HasNextPage {
			return n, nil
		}
		after = prs.PageInfo.EndCursor
	}
}
