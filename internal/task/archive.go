package task

import (
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Archived is a task moved out of the active list. Archived tasks live in their own file so
// tasks.yaml (which the TUI reloads and polls) stays small however much history accumulates.
type Archived struct {
	Task       `yaml:",inline"`
	ArchivedAt time.Time `yaml:"archived_at"`
}

type archiveFile struct {
	Tasks []Archived `yaml:"tasks"`
}

func LoadArchive(path string) ([]Archived, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var f archiveFile
	if err := yaml.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return f.Tasks, nil
}

func SaveArchive(path string, a []Archived) error { return writeAtomic(path, archiveFile{Tasks: a}) }

// Tasks strips the archive metadata, e.g. to reuse Find or to check for id collisions.
func Tasks(a []Archived) []Task {
	out := make([]Task, len(a))
	for i, x := range a {
		out[i] = x.Task
	}
	return out
}

// Archive moves a task from the active file to the archive. The archive is written first, so a
// failure part-way leaves the task in both files rather than losing it.
func Archive(path, archivePath, key, repo string) (Task, error) {
	tasks, err := Load(path)
	if err != nil {
		return Task{}, err
	}
	i, err := Find(tasks, key, repo)
	if err != nil {
		return Task{}, err
	}
	arch, err := LoadArchive(archivePath)
	if err != nil {
		return Task{}, err
	}
	t := tasks[i]
	if err := SaveArchive(archivePath, append(arch, Archived{Task: t, ArchivedAt: time.Now().UTC().Truncate(time.Second)})); err != nil {
		return Task{}, err
	}
	return t, Save(path, append(tasks[:i:i], tasks[i+1:]...))
}

// Restore moves a task from the archive back to the active file. It keeps the task's id unless
// that id has since been reused, in which case it gets a fresh one.
func Restore(path, archivePath, key, repo string) (Task, error) {
	arch, err := LoadArchive(archivePath)
	if err != nil {
		return Task{}, err
	}
	i, err := Find(Tasks(arch), key, repo)
	if err != nil {
		return Task{}, err
	}
	tasks, err := Load(path)
	if err != nil {
		return Task{}, err
	}
	t := arch[i].Task
	if hasID(tasks, t.ID) {
		t.ID = NewID(append(tasks[:len(tasks):len(tasks)], Tasks(arch)...))
	}
	if err := Save(path, append(tasks, t)); err != nil {
		return Task{}, err
	}
	return t, SaveArchive(archivePath, append(arch[:i:i], arch[i+1:]...))
}

// SearchArchive returns the archived tasks matching every whitespace-separated word of query
// (case-insensitive, against all fields), newest archived first. An empty query matches all.
func SearchArchive(arch []Archived, query string) []Archived {
	words := strings.Fields(strings.ToLower(query))
	var out []Archived
	for i := len(arch) - 1; i >= 0; i-- {
		a := arch[i]
		hay := strings.ToLower(strings.Join([]string{a.ID, a.Label, a.Jira, a.PR, a.Repo, a.Dir, a.Session}, "\n"))
		match := true
		for _, w := range words {
			if !strings.Contains(hay, w) {
				match = false
				break
			}
		}
		if match {
			out = append(out, a)
		}
	}
	return out
}
