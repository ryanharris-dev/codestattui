package task

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Task links a JIRA ticket and/or a GitHub PR. Either may be empty.
type Task struct {
	ID    string `yaml:"id"`
	Label string `yaml:"label,omitempty"`
	Jira  string `yaml:"jira,omitempty"` // e.g. ABC-123
	PR    string `yaml:"pr,omitempty"`   // owner/repo#123
	// Session is a Claude Code session id; Dir is where it was started (sessions are per-directory).
	Session string `yaml:"session,omitempty"`
	Dir     string `yaml:"dir,omitempty"`
}

type file struct {
	Tasks []Task `yaml:"tasks"`
}

var (
	prURLRe = regexp.MustCompile(`github\.com/([^/]+/[^/]+)/pull/(\d+)`)
	prRefRe = regexp.MustCompile(`^([^/\s]+/[^#\s]+)#(\d+)$`)
	jiraRe  = regexp.MustCompile(`[A-Z][A-Z0-9]+-\d+`)
)

// ParsePR normalizes a PR URL or owner/repo#N into owner/repo#N.
func ParsePR(s string) (string, error) {
	s = strings.TrimSpace(s)
	if m := prURLRe.FindStringSubmatch(s); m != nil {
		return m[1] + "#" + m[2], nil
	}
	if prRefRe.MatchString(s) {
		return s, nil
	}
	return "", fmt.Errorf("invalid PR reference %q (want URL or owner/repo#N)", s)
}

// SplitPR returns repo and number from owner/repo#N.
func SplitPR(ref string) (repo string, num int) {
	m := prRefRe.FindStringSubmatch(ref)
	if m == nil {
		return "", 0
	}
	num, _ = strconv.Atoi(m[2])
	return m[1], num
}

// FindJiraKey extracts the first JIRA-style key from text (branch name, title).
func FindJiraKey(s string) string { return jiraRe.FindString(strings.ToUpper(s)) }

func (t Task) Display() string {
	switch {
	case t.Label != "":
		return t.Label
	case t.Jira != "":
		return t.Jira
	default:
		return t.PR
	}
}

func Load(path string) ([]Task, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var f file
	if err := yaml.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return f.Tasks, nil
}

// Save writes atomically (temp file + rename) so a running app never reads a partial file.
func Save(path string, tasks []Task) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := yaml.Marshal(file{Tasks: tasks})
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tasks-*.yaml")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	tmp.Close()
	return os.Rename(tmp.Name(), path)
}

// Find locates a task by id, jira key, or PR ref.
func Find(tasks []Task, key string) int {
	for i, t := range tasks {
		if t.ID == key || strings.EqualFold(t.Jira, key) || t.PR == key || (t.Label != "" && t.Label == key) {
			return i
		}
	}
	return -1
}

func NewID(tasks []Task) string {
	n := len(tasks) + 1
	for {
		id := strconv.Itoa(n)
		if Find(tasks, id) < 0 {
			return id
		}
		n++
	}
}

// Normalize validates fields and fills in the JIRA key from the PR when absent.
func Normalize(t *Task) error {
	if t.PR != "" {
		pr, err := ParsePR(t.PR)
		if err != nil {
			return err
		}
		t.PR = pr
	}
	if t.Jira != "" {
		t.Jira = strings.ToUpper(strings.TrimSpace(t.Jira))
		if !jiraRe.MatchString(t.Jira) {
			return fmt.Errorf("invalid JIRA key %q", t.Jira)
		}
	}
	if t.Session != "" && t.Dir == "" {
		wd, err := os.Getwd()
		if err != nil {
			return err
		}
		t.Dir = wd
	}
	if t.Jira == "" && t.PR == "" {
		return fmt.Errorf("task needs a --jira key or a --pr")
	}
	return nil
}
