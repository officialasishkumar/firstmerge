# firstmerge

What happens to a first-time contributor's pull request?

`firstmerge` collects every pull request opened in a set of GitHub repositories
and measures how first-time contributors fare: how many of their PRs are merged,
how long they wait for a person to answer, and what about a PR goes with it
being merged. It was built to answer that question for the CNCF's graduated and
incubating projects.

## Findings: 77 CNCF projects, PRs opened 24 May 2025 to 24 May 2026

Data collected 24 Nov 2026 from the main repository of every graduated and
incubating CNCF project (78; gRPC left out because it merges outside GitHub),
so every PR in the window had at least six months to be merged or closed.
53,454 human PRs; 19,722 bot PRs (27% of all PRs) excluded. Full output:
[results.md](results.md), [results.json](results.json).

| who opened it | PRs | merged |
|---|---:|---:|
| first-time contributor | 8,116 | **50%** |
| returning contributor without commit access | 22,962 | 70% |
| member or collaborator | 22,376 | 86% |

**Waiting for a person.** The median first-timer waited **17 hours** for the
first human review or comment; a quarter waited **4 days** or more. **18%**
never got a reply from a person, and for 15% the only replies came from bots
(CLA checks, CI, AI reviewers). Merged first-timer PRs took a median of
4.9 days; members' took 19 hours.

**An unanswered PR is a dead PR.**

| first human response | PRs | merged |
|---|---:|---:|
| within 24 hours | 3,704 | 64% |
| 1 to 7 days | 1,726 | 59% |
| more than 7 days | 1,197 | 46% |
| never | 1,489 | **5%** |

**Small wins.**

| lines added + deleted | PRs | merged |
|---|---:|---:|
| under 10 | 2,329 | 63% |
| 10 to 99 | 2,894 | 50% |
| 100 to 499 | 1,864 | 43% |
| 500 or more | 1,029 | 32% |

**Linking an issue doesn't help.** First-timer PRs that close a linked issue
were merged 47% of the time; PRs without one, 52%.

**Rejection doesn't drive people away as much as silence might:** 42% of
first-timers whose PR was merged opened another PR in the same project; so did
40% of those whose PR was closed.

Per project (selection; every project with 10+ first-timer PRs is in [results.md](results.md)):

| repository | first-timer PRs | merged | median first human response | never answered by a person |
|---|---:|---:|---:|---:|
| kubernetes/kubernetes | 870 | 34% | 18.4h | 33% |
| meshery/meshery | 689 | 64% | 1.8h | 18% |
| argoproj/argo-cd | 562 | 47% | 11.1h | 14% |
| backstage/backstage | 394 | 58% | 2.0d | 18% |
| keycloak/keycloak | 393 | 48% | 15.6h | 17% |
| cilium/cilium | 326 | 60% | 15.9h | 8% |
| prometheus/prometheus | 292 | 47% | 47.2h | 12% |
| envoyproxy/envoy | 284 | 60% | 23.4h | 13% |
| helm/helm | 246 | 39% | 6.3h | 15% |
| istio/istio | 182 | 56% | 4.1h | 9% |
| open-policy-agent/opa | 84 | 65% | 8.4h | 1% |
| strimzi/strimzi-kafka-operator | 62 | 69% | 54m | 5% |
| kubescape/kubescape | 45 | 87% | 2.5h | 4% |

These are associations, not causes: a PR nobody answers may also be a PR nobody
could have merged. But the pattern holds across projects of every size, and the
cheapest intervention, a human reply within a day, is one every project can make.

## Install and run

```
go install github.com/officialasishkumar/firstmerge@latest

export GITHUB_TOKEN=...                       # any token; public data only
firstmerge collect -repos repos-cncf.txt -since 2025-03-01 -out prs.jsonl
firstmerge report prs.jsonl                   # Markdown
firstmerge report -json prs.jsonl             # everything, as JSON
firstmerge report -skip grpc/grpc prs.jsonl   # leave out repos that merge outside GitHub
```

`collect` uses the GraphQL API, streams each repository to disk, logs progress
and resumes where it stopped. The 78 CNCF repositories (152,000 PRs since March 2025) took a few hours on
one token, limited by GitHub's secondary rate limits. `repos-cncf.txt` is the
list used here, taken from the CNCF landscape (graduated and incubating
projects, main repository each).

## How it decides

- **Bots** are accounts of type Bot or whose login looks like automation
  (`*[bot]`, `*-robot`, `k8s-ci-robot`, `easycla`, `codecov`, `coderabbit`, `l5io`, ...).
  Their PRs are excluded and their comments don't count as a response.
- **First-time contributor:** the author's earliest PR in that repository in the
  collected data, opened at least 60 days after the data begins (so earlier
  activity is visible), from someone who is not a member, owner or collaborator.
  Someone whose last PR was more than about 15 months earlier can be counted as
  new; that is the main limit of the method.
- **First human response:** the earliest review or comment by anyone other than
  the author and bots (first six of each are fetched, oldest first).
- **Cohort:** PRs opened in the year ending six months before the newest PR,
  so outcomes have settled. Override with `-from` and `-to`.
- **Merged** means GitHub recorded a merge. Projects that import PRs through
  another system (gRPC uses Copybara) look like 0% and should be skipped.

## License

MIT
