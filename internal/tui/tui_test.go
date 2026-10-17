package tui

import (
	"strings"
	"testing"

	"github.com/ryanharris/codestattui/internal/config"
	"github.com/ryanharris/codestattui/internal/provider"
	"github.com/ryanharris/codestattui/internal/task"
)

func TestAllGood(t *testing.T) {
	m := New(config.Default(), "")
	good := provider.Result{
		Jira: &provider.JiraInfo{Status: "qa approved"},
		PR:   &provider.PRInfo{Approval: provider.Good, CI: provider.Good, Merge: provider.Good},
	}
	if !m.allGood(good) {
		t.Error("expected all good")
	}
	good.PR.CI = provider.Pending
	if m.allGood(good) {
		t.Error("pending CI must not be all good")
	}
}

func TestNarrowView(t *testing.T) {
	m := New(config.Default(), "")
	m.w, m.h = 30, 10
	m.tasks = []task.Task{{ID: "1", Jira: "ABC-1"}}
	m.results["1"] = provider.Result{Jira: &provider.JiraInfo{Summary: strings.Repeat("long ", 20), Status: "Open"}}
	if out := m.View(); !strings.Contains(out, "ABC-1") {
		t.Errorf("missing row:\n%s", out)
	}
}
