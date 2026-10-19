package tui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
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
	openDoneMsg   struct{ err error }
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
	help      bool
	mode      mode
	field     task.Field // field being edited in modeEdit
	input     []rune
	w, h      int
}

type mode int

const (
	modeNormal mode = iota
	modeUpdate      // "u" pressed; waiting for a field key
	modeEdit        // typing a new value
)

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
	case openDoneMsg:
		m.notice = "open failed: " + msg.err.Error()
	case tea.KeyMsg:
		m.notice = ""
		if m.help { // any key dismisses help
			m.help = false
			return m, nil
		}
		switch m.mode {
		case modeUpdate:
			return m.updateChord(msg)
		case modeEdit:
			return m.editKey(msg)
		}
		switch msg.String() {
		case "?":
			m.help = true
		case "u":
			if m.cursor < len(m.tasks) {
				m.mode = modeUpdate
			}
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
		case "J":
			key := m.jiraKey()
			if key == "" {
				m.notice = "no jira ticket on this task"
				return m, nil
			}
			// acli opens the ticket on the site it is logged in to.
			return m, openCmd("acli", "jira", "workitem", "view", key, "--web")
		case "P":
			url := m.prLink()
			if url == "" {
				m.notice = "no PR on this task"
				return m, nil
			}
			return m, openCmd(opener(), url)
		case "q", "ctrl+c":
			return m, tea.Quit
		case "j", "w", "down":
			if m.cursor < len(m.tasks)-1 {
				m.cursor++
			}
		case "k", "t", "up":
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

// jiraKey is the selected task's ticket key ("" if none), falling back to one found from the PR.
func (m Model) jiraKey() string {
	if m.cursor >= len(m.tasks) {
		return ""
	}
	t := m.tasks[m.cursor]
	if t.Jira != "" {
		return t.Jira
	}
	return m.results[t.ID].JiraKey
}

// prLink is the GitHub URL of the selected task's PR ("" if none).
func (m Model) prLink() string {
	if m.cursor >= len(m.tasks) {
		return ""
	}
	repo, n := task.SplitPR(m.tasks[m.cursor].PR)
	if n == 0 {
		return ""
	}
	return fmt.Sprintf("https://github.com/%s/pull/%d", repo, n)
}

func opener() string {
	if runtime.GOOS == "darwin" {
		return "open"
	}
	return "xdg-open"
}

// openCmd runs a browser-opening command in the background and reports failure as a notice.
func openCmd(name string, args ...string) tea.Cmd {
	return func() tea.Msg {
		if err := exec.Command(name, args...).Run(); err != nil {
			return openDoneMsg{err}
		}
		return nil
	}
}

// updateChord handles the key after "u", choosing which field to edit.
func (m Model) updateChord(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	m.mode = modeNormal
	f, ok := task.FieldByKey(msg.String())
	if !ok {
		if msg.String() != "esc" {
			m.notice = "no field bound to " + msg.String()
		}
		return m, nil
	}
	m.mode, m.field = modeEdit, f
	m.input = []rune(f.Get(m.tasks[m.cursor]))
	return m, nil
}

// editKey handles typing into the value prompt. enter saves, esc cancels; an empty value clears the field.
func (m Model) editKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEsc, tea.KeyCtrlC:
		m.mode = modeNormal
	case tea.KeyEnter:
		m.mode = modeNormal
		if err := task.UpdateField(m.path, m.tasks[m.cursor].ID, m.field, string(m.input)); err != nil {
			m.notice = "update failed: " + err.Error()
			return m, nil
		}
		m.mtime = time.Time{} // force reload; it refetches if jira/pr changed
		return m, m.reload()
	case tea.KeyBackspace:
		if len(m.input) > 0 {
			m.input = m.input[:len(m.input)-1]
		}
	case tea.KeyCtrlU:
		m.input = nil
	case tea.KeySpace:
		m.input = append(m.input, ' ')
	case tea.KeyRunes:
		m.input = append(m.input, msg.Runes...)
	}
	return m, nil
}

// updateHelp lists the chord keys, generated from task.Fields.
func updateHelp() string {
	var parts []string
	for _, f := range task.Fields {
		parts = append(parts, f.Key+"="+f.Name)
	}
	return strings.Join(parts, " ")
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
	cyan   = lipgloss.NewStyle().Foreground(lipgloss.Color("6"))
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
	if m.help {
		return m.helpView()
	}
	var b strings.Builder
	b.WriteString(bold.Render("codestattui") + "\n")
	b.WriteString(dim.Render("  J P C M D ✓") + "\n")
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
	switch m.mode {
	case modeUpdate:
		b.WriteString("\n" + cyan.Render("update: ") + dim.Render(updateHelp()+" · esc cancel"))
		return b.String()
	case modeEdit:
		b.WriteString("\n" + cyan.Render(m.field.Name+": ") + string(m.input) + "█" + dim.Render("  enter save · esc cancel"))
		return b.String()
	}
	b.WriteString("\n" + dim.Render("j/k or w/t move · enter detail · u update · o resume claude · J jira · P PR · r refresh · ? help · q quit"))
	return b.String()
}

func (m Model) helpView() string {
	rows := []string{
		bold.Render("Columns"),
		"  J  jira ticket is in a ready status",
		"  P  PR review / approval",
		"  C  PR CI checks",
		"  M  PR is mergeable",
		"  D  all checks on the merge commit (after merge)",
		"  ✓  everything is ready to merge",
		"",
		bold.Render("Symbols"),
		"  " + icon(provider.Good) + "  ok",
		"  " + icon(provider.Pending) + "  pending",
		"  " + icon(provider.Bad) + "  failing",
		"  " + icon(provider.Unknown) + "  n/a",
		"  " + red.Render("!") + "  error fetching jira or PR",
		"  " + dim.Render("…") + "  loading",
		"",
		bold.Render("Keys"),
		"  j/k or w/t  move",
		"  enter       toggle detail",
		"  u           update a field (" + updateHelp() + ")",
		"  o           resume claude session",
		"  J           open jira ticket in browser",
		"  P           open PR in browser",
		"  r           refresh",
		"  ?           this help",
		"  q           quit",
		"",
		dim.Render("press any key to close"),
	}
	return bold.Render("codestattui help") + "\n\n" + strings.Join(rows, "\n")
}

func (m Model) row(t task.Task, selected bool) string {
	r, have := m.results[t.ID]
	icons := "○ ○ ○ ○ ○ "
	mark := " "
	title := ""
	if have {
		pr := provider.PRInfo{}
		if r.PR != nil {
			pr = *r.PR
		}
		icons = fmt.Sprintf("%s %s %s %s %s ", icon(m.jiraState(r)),
			icon(pr.Approval), icon(pr.CI), icon(pr.Merge), icon(pr.Post))
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
	repo := ""
	if t.Repo != "" {
		repo = " " + cyan.Render("("+t.Repo+")")
	}
	idw := 0 // pad ids to the widest so labels stay aligned
	for _, o := range m.tasks {
		idw = max(idw, lipgloss.Width(o.ID))
	}
	id := dim.Render(fmt.Sprintf("%*s", idw, t.ID)) + " "
	// layout: caret(2) icons(10) mark(2) id(idw+1) name title(rest) repo
	fixed := 2 + lipgloss.Width(name) + 1 + 10 + 2 + idw + 1 + lipgloss.Width(repo)
	avail := m.w - fixed
	if avail > 3 && title != "" {
		title = " " + dim.Render(trunc(title, avail-1))
	} else {
		title = ""
	}
	caret := " "
	if selected {
		caret = "▸"
	}
	return fmt.Sprintf("%s %s%s %s%s%s%s", caret, icons, mark, id, name, title, repo)
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
			if r.PR.Merged {
				add("    ", "post-merge: "+r.PR.PostText)
			}
			add("    ", "branch: "+r.PR.Branch)
		} else if r.PRErr != nil {
			add("PR  ", red.Render(r.PRErr.Error()))
		}
	}
	if t.Repo != "" {
		add("repo", t.Repo)
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
