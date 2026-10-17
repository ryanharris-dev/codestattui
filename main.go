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
  codestattui add [--jira KEY] [--pr URL|owner/repo#N] [--label TEXT]
  codestattui update <id|jira|pr> [--jira KEY] [--pr URL] [--label TEXT]
  codestattui rm <id|jira|pr>

Files live in $CODESTATTUI_HOME or ~/.config/codestattui (tasks.yaml, config.yaml).
`

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	path := config.TasksPath()
	if len(args) == 0 {
		cfg, err := config.Load()
		if err != nil {
			return fmt.Errorf("config.yaml: %w", err)
		}
		return tui.Run(cfg, path)
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
			fmt.Printf("%s\t%s\t%s\t%s\n", t.ID, t.Jira, t.PR, t.Label)
		}
		return nil
	case "add":
		fs := flag.NewFlagSet("add", flag.ContinueOnError)
		var t task.Task
		fs.StringVar(&t.Jira, "jira", "", "JIRA key")
		fs.StringVar(&t.PR, "pr", "", "PR URL or owner/repo#N")
		fs.StringVar(&t.Label, "label", "", "display label")
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
		t.ID = task.NewID(tasks)
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
		if err := fs.Parse(args[2:]); err != nil {
			return err
		}
		tasks, err := task.Load(path)
		if err != nil {
			return err
		}
		i := task.Find(tasks, args[1])
		if i < 0 {
			return fmt.Errorf("no task matching %q", args[1])
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
		i := task.Find(tasks, args[1])
		if i < 0 {
			return fmt.Errorf("no task matching %q", args[1])
		}
		return task.Save(path, append(tasks[:i], tasks[i+1:]...))
	}
	return fmt.Errorf("unknown command %q\n\n%s", args[0], strings.TrimSpace(usage))
}

func dup(tasks []task.Task, t task.Task) int {
	for i, o := range tasks {
		if (t.Jira != "" && strings.EqualFold(o.Jira, t.Jira)) || (t.PR != "" && o.PR == t.PR) {
			return i
		}
	}
	return -1
}
