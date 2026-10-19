package task

import (
	"path/filepath"
	"testing"
)

func TestArchiveRestoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path, arch := filepath.Join(dir, "tasks.yaml"), filepath.Join(dir, "archive.yaml")
	if err := Save(path, []Task{{ID: "1", Jira: "ABC-1", Repo: "api"}, {ID: "2", Jira: "ABC-2", Label: "login fix"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := Archive(path, arch, "ABC-1", ""); err != nil {
		t.Fatal(err)
	}
	active, _ := Load(path)
	archived, _ := LoadArchive(arch)
	if len(active) != 1 || active[0].ID != "2" || len(archived) != 1 || archived[0].ID != "1" || archived[0].ArchivedAt.IsZero() {
		t.Fatalf("after archive: active=%v archived=%v", active, archived)
	}

	// The id was reused while archived; restore must not collide.
	if err := Save(path, append(active, Task{ID: "1", Jira: "ABC-9"})); err != nil {
		t.Fatal(err)
	}
	r, err := Restore(path, arch, "ABC-1", "")
	if err != nil {
		t.Fatal(err)
	}
	if r.ID == "1" || r.Jira != "ABC-1" {
		t.Fatalf("restored %+v, want fresh id", r)
	}
	if active, _ = Load(path); len(active) != 3 {
		t.Fatalf("active=%v", active)
	}
	if archived, _ = LoadArchive(arch); len(archived) != 0 {
		t.Fatalf("archived=%v", archived)
	}
}

func TestSearchArchive(t *testing.T) {
	a := []Archived{
		{Task: Task{ID: "1", Jira: "ABC-1", Repo: "api"}},
		{Task: Task{ID: "2", Jira: "ABC-2", Label: "Login Fix", Repo: "web"}},
	}
	if got := SearchArchive(a, ""); len(got) != 2 || got[0].ID != "2" {
		t.Fatalf("empty query should return all, newest first: %v", got)
	}
	if got := SearchArchive(a, "login WEB"); len(got) != 1 || got[0].ID != "2" {
		t.Fatalf("multi-word AND, case-insensitive: %v", got)
	}
	if got := SearchArchive(a, "login api"); len(got) != 0 {
		t.Fatalf("all words must match: %v", got)
	}
}
