package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/ryanharris/codestattui/internal/task"
)

// State is a tri-state indicator.
type State int

const (
	Unknown State = iota
	Good
	Pending
	Bad
)

type PRInfo struct {
	Title        string
	Branch       string
	Closed       bool
	Merged       bool
	Approval     State
	ApprovalText string
	CI           State
	CIText       string
	Merge        State
	MergeText    string
	// Post is the combined state of the workflows triggered by the merge commit.
	Post     State
	PostText string
}

type JiraInfo struct {
	Summary string
	Status  string
}

// Result is the polled data for one task. Errors are per-source so one failure doesn't blank the row.
type Result struct {
	PR      *PRInfo
	PRErr   error
	Jira    *JiraInfo
	JiraErr error
	// JiraKey is the key used, possibly parsed from the PR.
	JiraKey string
	Fetched time.Time
}

func run(ctx context.Context, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && len(ee.Stderr) > 0 {
			return nil, fmt.Errorf("%s: %s", name, strings.TrimSpace(string(ee.Stderr)))
		}
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	return out, nil
}

// Fetch polls GitHub and JIRA for one task.
func Fetch(ctx context.Context, t task.Task) Result {
	r := Result{JiraKey: t.Jira, Fetched: time.Now()}
	if t.PR != "" {
		r.PR, r.PRErr = FetchPR(ctx, t.PR)
		if r.JiraKey == "" && r.PR != nil {
			r.JiraKey = task.FindJiraKey(r.PR.Branch)
			if r.JiraKey == "" {
				r.JiraKey = task.FindJiraKey(r.PR.Title)
			}
		}
	}
	if r.JiraKey != "" {
		r.Jira, r.JiraErr = FetchJira(ctx, r.JiraKey)
	}
	return r
}

type ghCheck struct {
	Typename   string `json:"__typename"`
	Status     string `json:"status"`     // CheckRun
	Conclusion string `json:"conclusion"` // CheckRun
	State      string `json:"state"`      // StatusContext
}

type ghPR struct {
	Title             string    `json:"title"`
	HeadRefName       string    `json:"headRefName"`
	State             string    `json:"state"`
	ReviewDecision    string    `json:"reviewDecision"`
	Mergeable         string    `json:"mergeable"`
	MergeStateStatus  string    `json:"mergeStateStatus"`
	StatusCheckRollup []ghCheck `json:"statusCheckRollup"`
	MergeCommit       struct {
		OID string `json:"oid"`
	} `json:"mergeCommit"`
}

func FetchPR(ctx context.Context, ref string) (*PRInfo, error) {
	repo, num := task.SplitPR(ref)
	if repo == "" {
		return nil, fmt.Errorf("bad PR ref %q", ref)
	}
	out, err := run(ctx, "gh", "pr", "view", fmt.Sprint(num), "--repo", repo, "--json",
		"title,headRefName,state,reviewDecision,mergeable,mergeStateStatus,statusCheckRollup,mergeCommit")
	if err != nil {
		return nil, err
	}
	var p ghPR
	if err := json.Unmarshal(out, &p); err != nil {
		return nil, err
	}
	info := &PRInfo{Title: p.Title, Branch: p.HeadRefName, Closed: p.State == "CLOSED", Merged: p.State == "MERGED"}

	switch p.ReviewDecision {
	case "APPROVED":
		info.Approval, info.ApprovalText = Good, "approved"
	case "CHANGES_REQUESTED":
		info.Approval, info.ApprovalText = Bad, "changes requested"
	case "REVIEW_REQUIRED":
		info.Approval, info.ApprovalText = Pending, "review required"
	default: // no review policy configured
		info.Approval, info.ApprovalText = Pending, "no review"
	}

	info.CI, info.CIText = rollup(p.StatusCheckRollup)
	if info.Merged && p.MergeCommit.OID != "" {
		info.Post, info.PostText = fetchPostMerge(ctx, repo, p.MergeCommit.OID)
	}

	switch {
	case info.Merged:
		// GitHub reports mergeable as UNKNOWN once a PR is merged.
		info.Merge, info.MergeText = Good, "merged"
	case p.Mergeable == "CONFLICTING" || p.MergeStateStatus == "DIRTY":
		info.Merge, info.MergeText = Bad, "conflict"
	case p.Mergeable == "UNKNOWN":
		info.Merge, info.MergeText = Pending, "checking"
	case p.MergeStateStatus == "BEHIND":
		info.Merge, info.MergeText = Pending, "behind base"
	case p.MergeStateStatus == "BLOCKED":
		info.Merge, info.MergeText = Pending, "blocked"
	case p.Mergeable == "MERGEABLE":
		info.Merge, info.MergeText = Good, "can merge"
	default:
		info.Merge, info.MergeText = Unknown, strings.ToLower(p.MergeStateStatus)
	}
	return info, nil
}

// fetchPostMerge combines every check run and commit status on the merge commit, which
// covers all workflows the merge spawned on the base branch.
func fetchPostMerge(ctx context.Context, repo, sha string) (State, string) {
	var checks []ghCheck
	out, err := run(ctx, "gh", "api", fmt.Sprintf("repos/%s/commits/%s/check-runs?per_page=100", repo, sha))
	if err != nil {
		return Unknown, err.Error()
	}
	var runs struct {
		CheckRuns []struct {
			Status     string `json:"status"`
			Conclusion string `json:"conclusion"`
		} `json:"check_runs"`
	}
	if err := json.Unmarshal(out, &runs); err != nil {
		return Unknown, err.Error()
	}
	for _, c := range runs.CheckRuns {
		checks = append(checks, ghCheck{Status: strings.ToUpper(c.Status), Conclusion: strings.ToUpper(c.Conclusion)})
	}
	if out, err := run(ctx, "gh", "api", fmt.Sprintf("repos/%s/commits/%s/status", repo, sha)); err == nil {
		var st struct {
			Statuses []struct {
				State string `json:"state"`
			} `json:"statuses"`
		}
		if json.Unmarshal(out, &st) == nil {
			for _, s := range st.Statuses {
				checks = append(checks, ghCheck{Typename: "StatusContext", State: strings.ToUpper(s.State)})
			}
		}
	}
	return rollup(checks)
}

func rollup(checks []ghCheck) (State, string) {
	if len(checks) == 0 {
		return Unknown, "no checks"
	}
	pending, failed := 0, 0
	for _, c := range checks {
		if c.Typename == "StatusContext" {
			switch c.State {
			case "SUCCESS":
			case "PENDING", "EXPECTED":
				pending++
			default:
				failed++
			}
			continue
		}
		if c.Status != "COMPLETED" {
			pending++
			continue
		}
		switch c.Conclusion {
		case "SUCCESS", "NEUTRAL", "SKIPPED", "CANCELLED": // cancelled is usually a superseded run, not a failure
		default:
			failed++
		}
	}
	switch {
	case failed > 0:
		return Bad, fmt.Sprintf("%d failing", failed)
	case pending > 0:
		return Pending, fmt.Sprintf("%d running", pending)
	}
	return Good, "passing"
}

func FetchJira(ctx context.Context, key string) (*JiraInfo, error) {
	out, err := run(ctx, "acli", "jira", "workitem", "view", key, "--json", "--fields", "summary,status")
	if err != nil {
		return nil, err
	}
	var v struct {
		Fields struct {
			Summary string `json:"summary"`
			Status  struct {
				Name string `json:"name"`
			} `json:"status"`
		} `json:"fields"`
	}
	if err := json.Unmarshal(out, &v); err != nil {
		return nil, err
	}
	if v.Fields.Status.Name == "" {
		return nil, fmt.Errorf("no status in acli output for %s", key)
	}
	return &JiraInfo{Summary: v.Fields.Summary, Status: v.Fields.Status.Name}, nil
}
