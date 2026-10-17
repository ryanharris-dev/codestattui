package tui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/ryanharris/codestattui/internal/config"
	"github.com/ryanharris/codestattui/internal/provider"
	"github.com/ryanharris/codestattui/internal/task"
)

type (
	pollMsg       struct{}
	reloadMsg     struct{}
	resumeDoneMsg struct{ err error }
	resultMsg     struct {
		id  string
		fp  string
		res provider.Result
	}
)

type Model struct {
	cfg       config.Config
	path      string
	tasks     []task.Task
	results   map[string]provider.Result
	fetchedFP map[string]string // task id -> fingerprint of what was fetched
	inflight  map[string]bool
	mtime     time.Time
	loadErr   error
	notice    string
	cursor    int
	detail    bool
	w, h      int
}

func New(cfg config.Config, path string) Model {
	return Model{cfg: cfg, path: path, results: map[string]provider.Result{},
		fetchedFP: map[string]string{}, inflight: map[string]bool{}}
}

func fingerprint(t task.Task) string { return t.Jira + "|" + t.PR }

func (m Model) Init() tea.Cmd {
	return tea.Batch(func() tea.Msg { return reloadMsg{} }, pollTick(m.cfg.PollInterval), reloadTick())
}

func pollTick(d time.Duration) tea.Cmd {
	return tea.Tick(d, func(time.Time) tea.Msg { return pollMsg{} })
}

func reloadTick() tea.Cmd {
	return tea.Tick(2*time.Second, func(time.Time) tea.Msg { return reloadMsg{} })
}

func fetchCmd(t task.Task) tea.Cmd {
	return func() tea.Msg {
		return resultMsg{id: t.ID, fp: fingerprint(t), res: provider.Fetch(context.Background(), t)}
	}
}

// fetchAll starts a fetch for every task not already in flight (or only stale ones if staleOnly).
func (m *Model) fetchTasks(staleOnly bool) tea.Cmd {
	var cmds []tea.Cmd
	for _, t := range m.tasks {
		if m.inflight[t.ID] {
			continue
		}
		if staleOnly && m.fetchedFP[t.ID] == fingerprint(t) {
			continue
		}
		m.inflight[t.ID] = true
		cmds = append(cmds, fetchCmd(t))
	}
	return tea.Batch(cmds...)
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
	case reloadMsg:
		cmd := m.reload()
		return m, tea.Batch(cmd, reloadTick())
	case pollMsg:
		return m, tea.Batch(m.fetchTasks(false), pollTick(m.cfg.PollInterval))
	case resultMsg:
		delete(m.inflight, msg.id)
		m.results[msg.id] = msg.res
		m.fetchedFP[msg.id] = msg.fp
	case resumeDoneMsg:
		if msg.err != nil {
			m.notice = "resume failed: " + msg.err.Error()
		}
		return m, m.fetchTasks(false)
	case tea.KeyMsg:
		m.notice = ""
		switch msg.String() {
		case "o":
			if m.cursor >= len(m.tasks) {
				return m, nil
			}
			t := m.tasks[m.cursor]
			if t.Session == "" {
				m.notice = "no Claude session on this task (add with --session)"
				return m, nil
			}
			c := exec.Command("claude", "--resume", t.Session)
			c.Dir = t.Dir
			return m, tea.ExecProcess(c, func(err error) tea.Msg { return resumeDoneMsg{err} })
		case "q", "ctrl+c":
			return m, tea.Quit
		case "j", "down":
			if m.cursor < len(m.tasks)-1 {
				m.cursor++
			}
		case "k", "up":
			if m.cursor > 0 {
				m.cursor--
			}
		case "enter":
			m.detail = !m.detail
		case "esc":
			m.detail = false
		case "r":
			return m, m.fetchTasks(false)
		}
	}
	return m, nil
}

// reload re-reads tasks.yaml if it changed on disk.
func (m *Model) reload() tea.Cmd {
	st, err := os.Stat(m.path)
	if err != nil {
		if os.IsNotExist(err) {
			m.tasks, m.loadErr, m.mtime = nil, nil, time.Time{}
		} else {
			m.loadErr = err
		}
		return nil
	}
	if st.ModTime().Equal(m.mtime) {
		return nil
	}
	tasks, err := task.Load(m.path)
	if err != nil {
		m.loadErr = err // keep showing the last good list
		return nil
	}
	m.mtime, m.loadErr, m.tasks = st.ModTime(), nil, tasks
	if m.cursor >= len(tasks) {
		m.cursor = max(0, len(tasks)-1)
	}
	return m.fetchTasks(true)
}

// ---- styles & rendering ----

var (
	green  = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	yellow = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	red    = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	dim    = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	bold   = lipgloss.NewStyle().Bold(true)
)

func icon(s provider.State) string {
	switch s {
	case provider.Good:
		return green.Render("●")
	case provider.Pending:
		return yellow.Render("◐")
	case provider.Bad:
		return red.Render("✖")
	}
	return dim.Render("○")
}

func (m Model) jiraState(r provider.Result) provider.State {
	if r.Jira == nil {
		return provider.Unknown
	}
	if m.isReady(r.Jira.Status) {
		return provider.Good
	}
	return provider.Pending
}

func (m Model) isReady(status string) bool {
	for _, s := range m.cfg.ReadyStatuses {
		if strings.EqualFold(s, status) {
			return true
		}
	}
	return false
}

// allGood: ticket in a ready status and the PR is approved, green, and mergeable.
func (m Model) allGood(r provider.Result) bool {
	return r.Jira != nil && r.PR != nil && m.isReady(r.Jira.Status) &&
		!r.PR.Closed && !r.PR.Merged &&
		r.PR.Approval == provider.Good && r.PR.CI == provider.Good && r.PR.Merge == provider.Good
}

func (m Model) View() string {
	if m.w == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(bold.Render("codestattui") + dim.Render("  J=jira P=review C=ci M=merge ✓=ready\n● ok  ◐ pending  ✖ failing  ○ n/a") + "\n")
	if m.loadErr != nil {
		b.WriteString(red.Render(trunc("tasks: "+m.loadErr.Error(), m.w)) + "\n")
	}
	if len(m.tasks) == 0 {
		b.WriteString(dim.Render("\nNo tasks. Add one:\n  codestattui add --jira ABC-1 --pr owner/repo#2"))
		return b.String()
	}
	for i, t := range m.tasks {
		b.WriteString(m.row(t, i == m.cursor) + "\n")
	}
	if m.detail && m.cursor < len(m.tasks) {
		b.WriteString("\n" + m.detailView(m.tasks[m.cursor]))
	}
	if m.notice != "" {
		b.WriteString("\n" + yellow.Render(trunc(m.notice, m.w)))
	}
	b.WriteString("\n" + dim.Render("j/k move · enter detail · o resume claude · r refresh · q quit"))
	return b.String()
}

func (m Model) row(t task.Task, selected bool) string {
	r, have := m.results[t.ID]
	icons := "○ ○ ○ ○ "
	mark := " "
	title := ""
	if have {
		pr := provider.PRInfo{}
		if r.PR != nil {
			pr = *r.PR
		}
		icons = fmt.Sprintf("%s %s %s %s ", icon(m.jiraState(r)),
			icon(pr.Approval), icon(pr.CI), icon(pr.Merge))
		if m.allGood(r) {
			mark = green.Render("✓")
		}
		switch {
		case r.Jira != nil:
			title = r.Jira.Summary
		case r.PR != nil:
			title = r.PR.Title
		}
		if r.PRErr != nil || r.JiraErr != nil {
			mark = red.Render("!")
		}
	} else if m.inflight[t.ID] {
		mark = dim.Render("…")
	}
	name := t.Display()
	// layout: caret(2) icons(8) mark(2) name title(rest)
	fixed := 2 + lipgloss.Width(name) + 1 + 8 + 2
	avail := m.w - fixed
	if avail > 3 && title != "" {
		title = dim.Render(trunc(title, avail))
	} else {
		title = ""
	}
	caret := " "
	if selected {
		caret = "▸"
	}
	return fmt.Sprintf("%s %s%s %s %s", caret, icons, mark, name, title)
}

func (m Model) detailView(t task.Task) string {
	r, ok := m.results[t.ID]
	if !ok {
		return dim.Render("loading…")
	}
	var lines []string
	add := func(k, v string) { lines = append(lines, fmt.Sprintf("%s %s", bold.Render(k), v)) }
	if r.JiraKey != "" {
		if r.Jira != nil {
			add("JIRA", fmt.Sprintf("%s  %s", r.JiraKey, r.Jira.Status))
			add("    ", r.Jira.Summary)
		} else if r.JiraErr != nil {
			add("JIRA", red.Render(r.JiraErr.Error()))
		}
	}
	if t.PR != "" {
		if r.PR != nil {
			add("PR  ", t.PR+"  "+r.PR.Title)
			add("    ", fmt.Sprintf("review: %s · ci: %s · merge: %s", r.PR.ApprovalText, r.PR.CIText, r.PR.MergeText))
			add("    ", "branch: "+r.PR.Branch)
		} else if r.PRErr != nil {
			add("PR  ", red.Render(r.PRErr.Error()))
		}
	}
	if t.Session != "" {
		add("claude", fmt.Sprintf("%s  (%s)", t.Session, t.Dir))
	}
	add("polled", r.Fetched.Format("15:04:05"))
	return wrap(strings.Join(lines, "\n"), m.w)
}

func wrap(s string, w int) string {
	return lipgloss.NewStyle().Width(w).Render(s)
}

func trunc(s string, w int) string {
	if w <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= w {
		return s
	}
	if w == 1 {
		return "…"
	}
	return string(r[:w-1]) + "…"
}

func Run(cfg config.Config, path string) error {
	_, err := tea.NewProgram(New(cfg, path), tea.WithAltScreen()).Run()
	return err
}
