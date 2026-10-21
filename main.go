package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/ryanharris/codestattui/internal/config"
	"github.com/ryanharris/codestattui/internal/task"
	"github.com/ryanharris/codestattui/internal/tui"
)

const usage = `codestattui - task status from JIRA and GitHub

Usage:
  codestattui                       open the TUI
  codestattui list                  print tasks
  codestattui add [--jira KEY|URL] [--pr URL|owner/repo#N] [--label TEXT] [--session ID] [--dir PATH] [--repo NAME]
  codestattui update <id|jira|pr|label> [--jira KEY|URL] [--pr URL] [--label TEXT] [--session ID] [--dir PATH]
                                    [--repo NAME]   (selects among tasks sharing a ticket)
  codestattui rm <id|jira|pr|label> [--repo NAME]
  codestattui archive <id|jira|pr|label> [--repo NAME]     move a task out of the active list
  codestattui unarchive <id|jira|pr|label> [--repo NAME]   bring it back
  codestattui archived [search words...]                   list/search archived tasks

Files live in $CODESTATTUI_HOME or ~/.config/codestattui (tasks.yaml, archive.yaml, config.yaml).
`

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	path, archivePath := config.TasksPath(), config.ArchivePath()
	if len(args) == 0 {
		cfg, err := config.Load()
		if err != nil {
			return fmt.Errorf("config.yaml: %w", err)
		}
		return tui.Run(cfg, path, archivePath)
	}
	switch args[0] {
	case "help", "-h", "--help":
		fmt.Print(usage)
		return nil
	case "list":
		tasks, err := task.Load(path)
		if err != nil {
			return err
		}
		for _, t := range tasks {
			fmt.Printf("%s\t%s\t%s\t%s\t%s\t%s\n", t.ID, t.Jira, t.Repo, t.PR, t.Label, t.Session)
		}
		return nil
	case "add":
		fs := flag.NewFlagSet("add", flag.ContinueOnError)
		var t task.Task
		fs.StringVar(&t.Jira, "jira", "", "JIRA key")
		fs.StringVar(&t.PR, "pr", "", "PR URL or owner/repo#N")
		fs.StringVar(&t.Label, "label", "", "display label")
		fs.StringVar(&t.Session, "session", "", "Claude Code session id to resume from the TUI")
		fs.StringVar(&t.Dir, "dir", "", "directory the session was started in (default: cwd)")
		fs.StringVar(&t.Repo, "repo", "", "repository the work happens in (default: from --pr or --dir)")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if err := task.Normalize(&t); err != nil {
			return err
		}
		tasks, err := task.Load(path)
		if err != nil {
			return err
		}
		if i := dup(tasks, t); i >= 0 {
			return fmt.Errorf("task %s already tracks that ticket/PR", tasks[i].ID)
		}
		arch, err := task.LoadArchive(archivePath)
		if err != nil {
			return err
		}
		// Ids are never reused: skip those held by archived tasks too.
		t.ID = task.NewID(append(tasks[:len(tasks):len(tasks)], task.Tasks(arch)...))
		if err := task.Save(path, append(tasks, t)); err != nil {
			return err
		}
		fmt.Println("added", t.ID)
		return nil
	case "update":
		if len(args) < 2 {
			return fmt.Errorf("update needs a task id, jira key, or PR")
		}
		fs := flag.NewFlagSet("update", flag.ContinueOnError)
		jira := fs.String("jira", "", "JIRA key")
		pr := fs.String("pr", "", "PR URL or owner/repo#N")
		label := fs.String("label", "", "display label")
		session := fs.String("session", "", "Claude Code session id")
		dir := fs.String("dir", "", "session directory")
		repo := fs.String("repo", "", "select the task in this repo (does not change it)")
		if err := fs.Parse(args[2:]); err != nil {
			return err
		}
		tasks, err := task.Load(path)
		if err != nil {
			return err
		}
		i, err := task.Find(tasks, args[1], *repo)
		if err != nil {
			return err
		}
		set := map[string]bool{}
		fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
		t := tasks[i]
		if set["jira"] {
			t.Jira = *jira
		}
		if set["pr"] {
			t.PR = *pr
		}
		if set["label"] {
			t.Label = *label
		}
		if set["session"] {
			t.Session = *session
		}
		if set["dir"] {
			t.Dir = *dir
		}
		if err := task.Normalize(&t); err != nil {
			return err
		}
		tasks[i] = t
		return task.Save(path, tasks)
	case "rm":
		if len(args) < 2 {
			return fmt.Errorf("rm needs a task id, jira key, or PR")
		}
		tasks, err := task.Load(path)
		if err != nil {
			return err
		}
		fs := flag.NewFlagSet("rm", flag.ContinueOnError)
		repo := fs.String("repo", "", "select the task in this repo")
		if err := fs.Parse(args[2:]); err != nil {
			return err
		}
		i, err := task.Find(tasks, args[1], *repo)
		if err != nil {
			return err
		}
		return task.Save(path, append(tasks[:i], tasks[i+1:]...))
	case "archive", "unarchive":
		if len(args) < 2 {
			return fmt.Errorf("%s needs a task id, jira key, or PR", args[0])
		}
		fs := flag.NewFlagSet(args[0], flag.ContinueOnError)
		repo := fs.String("repo", "", "select the task in this repo")
		if err := fs.Parse(args[2:]); err != nil {
			return err
		}
		move := task.Archive
		if args[0] == "unarchive" {
			move = task.Restore
		}
		t, err := move(path, archivePath, args[1], *repo)
		if err != nil {
			return err
		}
		fmt.Printf("%sd %s (%s)\n", args[0], t.ID, t.Display())
		return nil
	case "archived":
		arch, err := task.LoadArchive(archivePath)
		if err != nil {
			return err
		}
		for _, a := range task.SearchArchive(arch, strings.Join(args[1:], " ")) {
			fmt.Printf("%s\t%s\t%s\t%s\t%s\t%s\t%s\n", a.ID, a.ArchivedAt.Local().Format("2006-01-02"), a.Jira, a.Repo, a.PR, a.Label, a.Session)
		}
		return nil
	}
	return fmt.Errorf("unknown command %q\n\n%s", args[0], strings.TrimSpace(usage))
}

func dup(tasks []task.Task, t task.Task) int {
	for i, o := range tasks {
		if (t.Jira != "" && strings.EqualFold(o.Jira, t.Jira) && o.Repo == t.Repo) || (t.PR != "" && o.PR == t.PR) {
			return i
		}
	}
	return -1
}
