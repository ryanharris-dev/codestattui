package task

import (
	"path/filepath"
	"testing"
)

func TestParsePR(t *testing.T) {
	for in, want := range map[string]string{
		"https://github.com/cli/cli/pull/12/files": "cli/cli#12",
		"cli/cli#12": "cli/cli#12",
	} {
		got, err := ParsePR(in)
		if err != nil || got != want {
			t.Errorf("ParsePR(%q)=%q,%v want %q", in, got, err, want)
		}
	}
	if _, err := ParsePR("nope"); err == nil {
		t.Error("expected error")
	}
}

func TestFindJiraKey(t *testing.T) {
	if got := FindJiraKey("feature/abc-123-do-thing"); got != "ABC-123" {
		t.Errorf("got %q", got)
	}
}

func TestNormalizeJiraURL(t *testing.T) {
	tk := Task{Jira: "https://x.atlassian.net/browse/cno-800"}
	if err := Normalize(&tk); err != nil || tk.Jira != "CNO-800" {
		t.Errorf("got %q, %v", tk.Jira, err)
	}
	bad := Task{Jira: "nonsense"}
	if err := Normalize(&bad); err == nil {
		t.Error("expected error")
	}
}

func TestFindByRepo(t *testing.T) {
	tasks := []Task{
		{ID: "1", Jira: "CNO-1", Repo: "fe"},
		{ID: "2", Jira: "CNO-1", Repo: "be"},
	}
	if _, err := Find(tasks, "CNO-1", ""); err == nil {
		t.Error("expected ambiguity error")
	}
	if i, err := Find(tasks, "cno-1", "be"); err != nil || i != 1 {
		t.Errorf("got %d, %v", i, err)
	}
	if i, err := Find(tasks, "1", ""); err != nil || i != 0 {
		t.Errorf("id lookup got %d, %v", i, err)
	}
	if _, err := Find(tasks, "CNO-2", ""); err == nil {
		t.Error("expected not found")
	}
}

func TestNormalizeRepoFromPR(t *testing.T) {
	tk := Task{PR: "https://github.com/org/connect1-backend/pull/3"}
	if err := Normalize(&tk); err != nil || tk.Repo != "connect1-backend" {
		t.Errorf("got %q, %v", tk.Repo, err)
	}
}

func TestUpdateField(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tasks.yaml")
	if err := Save(path, []Task{{ID: "1", Jira: "ABC-1"}}); err != nil {
		t.Fatal(err)
	}
	label, _ := FieldByKey("l")
	if err := UpdateField(path, "1", label, "  my label "); err != nil {
		t.Fatal(err)
	}
	jira, _ := FieldByKey("j")
	if err := UpdateField(path, "1", jira, ""); err == nil {
		t.Fatal("clearing the only ticket/PR should fail")
	}
	got, _ := Load(path)
	if got[0].Label != "my label" || got[0].Jira != "ABC-1" {
		t.Fatalf("got %+v", got[0])
	}
}
