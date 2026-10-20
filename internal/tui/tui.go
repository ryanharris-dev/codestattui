package tui

import (
	"bytes"
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
	spinMsg       struct{}
	resumeDoneMsg struct{ err error }
	openDoneMsg   struct{ err error }
	// splitDoneMsg reports a kitty split launch; on failure the resume falls back to running in place.
	splitDoneMsg struct {
		err error
		c   *exec.Cmd
	}
	resultMsg struct {
		id  string
		fp  string
		res provider.Result
	}
)

type Model struct {
	cfg       config.Config
	path      string
	archPath  string
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
	frame     int // spinner frame, advanced while fetches are in flight

	// Archive view: browses archive.yaml, which is never polled.
	archView   bool
	archived   []task.Archived
	archShown  []task.Archived // archived filtered by query, newest first
	archCursor int
	query      string

	confirmID, confirmName string // task awaiting archive confirmation
}

type mode int

const (
	modeNormal  mode = iota
	modeUpdate       // "u" pressed; waiting for a field key
	modeEdit         // typing a new value
	modeSearch       // typing an archive search query
	modeConfirm      // "a" pressed; waiting for y/n to archive confirmID
)

func New(cfg config.Config, path, archPath string) Model {
	return Model{cfg: cfg, path: path, archPath: archPath, results: map[string]provider.Result{},
		fetchedFP: map[string]string{}, inflight: map[string]bool{}}
}

func fingerprint(t task.Task) string { return t.Jira + "|" + t.PR }

func (m Model) Init() tea.Cmd {
	return tea.Batch(func() tea.Msg { return reloadMsg{} }, pollTick(m.cfg.PollInterval), reloadTick(), spinTick())
}

var spinFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

func spinTick() tea.Cmd {
	return tea.Tick(100*time.Millisecond, func(time.Time) tea.Msg { return spinMsg{} })
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
	case spinMsg:
		if len(m.inflight) > 0 {
			m.frame++
		}
		return m, spinTick()
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
	case splitDoneMsg:
		if msg.err != nil {
			m.notice = "kitty split failed, resuming in place: " + msg.err.Error()
			return m, tea.ExecProcess(msg.c, func(err error) tea.Msg { return resumeDoneMsg{err} })
		}
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
		case modeSearch:
			return m.searchKey(msg)
		case modeConfirm:
			return m.confirmKey(msg)
		}
		if m.archView {
			return m.archiveKey(msg)
		}
		switch msg.String() {
		case "?":
			m.help = true
		case "u":
			if m.cursor < len(m.tasks) {
				m.mode = modeUpdate
			}
		case "a":
			if m.cursor >= len(m.tasks) {
				return m, nil
			}
			t := m.tasks[m.cursor]
			m.mode, m.confirmID, m.confirmName = modeConfirm, t.ID, t.Display()
		case "A":
			m.openArchive()
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
			if id := os.Getenv("KITTY_WINDOW_ID"); id != "" {
				return m, kittySplitCmd(id, t, c)
			}
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

// confirmKey archives the pending task on y; any other key cancels. The task is addressed by
// id, so a reload that shifts the cursor can't make it archive a different one.
func (m Model) confirmKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	m.mode = modeNormal
	if msg.String() != "y" {
		return m, nil
	}
	t, err := task.Archive(m.path, m.archPath, m.confirmID, "")
	if err != nil {
		m.notice = "archive failed: " + err.Error()
		return m, nil
	}
	m.notice = "archived " + t.Display() + " (A to browse, U there to restore)"
	m.mtime = time.Time{}
	return m, m.reload()
}

// openArchive switches to the archive view, reading the archive fresh.
func (m *Model) openArchive() {
	arch, err := task.LoadArchive(m.archPath)
	if err != nil {
		m.notice = "archive: " + err.Error()
		return
	}
	m.archView, m.archived, m.query, m.archCursor = true, arch, "", 0
	m.refilter()
}

func (m *Model) refilter() {
	m.archShown = task.SearchArchive(m.archived, m.query)
	if m.archCursor >= len(m.archShown) {
		m.archCursor = max(0, len(m.archShown)-1)
	}
}

// archiveKey handles keys in the archive view.
func (m Model) archiveKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "?":
		m.help = true
	case "esc":
		if m.query != "" {
			m.query = ""
			m.refilter()
		} else {
			m.archView = false
		}
	case "A":
		m.archView = false
	case "/":
		m.mode, m.input = modeSearch, []rune(m.query)
	case "j", "w", "down":
		if m.archCursor < len(m.archShown)-1 {
			m.archCursor++
		}
	case "k", "t", "up":
		if m.archCursor > 0 {
			m.archCursor--
		}
	case "U":
		if m.archCursor >= len(m.archShown) {
			return m, nil
		}
		t, err := task.Restore(m.path, m.archPath, m.archShown[m.archCursor].ID, "")
		if err != nil {
			m.notice = "restore failed: " + err.Error()
			return m, nil
		}
		m.notice = "restored " + t.Display()
		arch, err := task.LoadArchive(m.archPath)
		if err != nil {
			m.notice = "archive: " + err.Error()
			return m, nil
		}
		m.archived = arch
		m.refilter()
		m.mtime = time.Time{}
		return m, m.reload()
	}
	return m, nil
}

// searchKey edits the archive query live. enter keeps it, esc clears it.
func (m Model) searchKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEsc, tea.KeyCtrlC:
		m.mode, m.query = modeNormal, ""
	case tea.KeyEnter:
		m.mode = modeNormal
	case tea.KeyBackspace:
		if len(m.input) > 0 {
			m.input = m.input[:len(m.input)-1]
		}
		m.query = string(m.input)
	case tea.KeyCtrlU:
		m.input, m.query = nil, ""
	case tea.KeySpace:
		m.input = append(m.input, ' ')
		m.query = string(m.input)
	case tea.KeyRunes:
		m.input = append(m.input, msg.Runes...)
		m.query = string(m.input)
	}
	m.archCursor = 0
	m.refilter()
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

// kittySplitCmd resumes the session in a new kitty window beside the TUI (the tall layout puts new
// windows to the right). It needs allow_remote_control in kitty.conf; fallback is the in-place command.
func kittySplitCmd(windowID string, t task.Task, fallback *exec.Cmd) tea.Cmd {
	args := []string{"@", "launch", "--type=window", "--location=vsplit", "--match=window_id:" + windowID}
	if t.Dir != "" {
		args = append(args, "--cwd="+t.Dir)
	}
	args = append(args, "claude", "--resume", t.Session)
	// kitten @ talks to kitty over /dev/tty, so it must run with the terminal released from bubbletea.
	var stderr bytes.Buffer
	c := exec.Command("kitten", args...)
	c.Stderr = &stderr
	return tea.ExecProcess(c, func(err error) tea.Msg {
		if err != nil {
			if msg := strings.TrimSpace(stderr.String()); msg != "" {
				err = fmt.Errorf("%s", msg)
			}
			return splitDoneMsg{err, fallback}
		}
		return nil
	})
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

func (m Model) isReady(status string) bool { return containsFold(m.cfg.ReadyStatuses, status) }

// isInQA: the ticket is waiting on QA, which is distinct from merely pending.
func (m Model) isInQA(status string) bool { return containsFold(m.cfg.QAStatuses, status) }

func containsFold(list []string, status string) bool {
	for _, s := range list {
		if strings.EqualFold(s, status) {
			return true
		}
	}
	return false
}

// jiraIcon is the J column: the usual state icon, but a cyan diamond while in QA.
func (m Model) jiraIcon(r provider.Result) string {
	if r.Jira != nil && !m.isReady(r.Jira.Status) && m.isInQA(r.Jira.Status) {
		return cyan.Render("◆")
	}
	return icon(m.jiraState(r))
}

// allGood: ticket in a ready status and the PR is approved, green, and mergeable.
func (m Model) allGood(r provider.Result) bool {
	return r.Jira != nil && r.PR != nil && m.isReady(r.Jira.Status) &&
		!r.PR.Closed && !r.PR.Merged &&
		r.PR.Approval == provider.Good && r.PR.CI == provider.Good && r.PR.Merge == provider.Good
}

// complete: the PR is merged, the ticket is in a ready status, and the post-merge checks
// haven't failed or are still running, so the task can be archived.
func (m Model) complete(r provider.Result) bool {
	return r.Jira != nil && r.PR != nil && r.PR.Merged && m.isReady(r.Jira.Status) &&
		r.PR.Post != provider.Bad && r.PR.Post != provider.Pending
}

func (m Model) View() string {
	if m.w == 0 {
		return ""
	}
	if m.help {
		return m.helpView()
	}
	if m.archView {
		return m.archiveView()
	}
	var b strings.Builder
	b.WriteString(bold.Render("codestattui"))
	if n := len(m.inflight); n > 0 {
		b.WriteString(" " + cyan.Render(spinFrames[m.frame%len(spinFrames)]) + dim.Render(fmt.Sprintf(" refreshing (%d)", n)))
	}
	b.WriteString("\n")
	b.WriteString(dim.Render("  J C P M D ✓") + "\n")
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
	case modeConfirm:
		b.WriteString("\n" + cyan.Render("archive ") + bold.Render(m.confirmName) + cyan.Render("? ") + dim.Render("y confirm · any other key cancels"))
		return b.String()
	case modeEdit:
		b.WriteString("\n" + cyan.Render(m.field.Name+": ") + string(m.input) + "█" + dim.Render("  enter save · esc cancel"))
		return b.String()
	}
	b.WriteString("\n" + dim.Render("j/k or w/t move · enter detail · u update · a archive · A archived · o resume claude · J jira · P PR · r refresh · ? help · q quit"))
	return b.String()
}

func (m Model) archiveView() string {
	var b strings.Builder
	b.WriteString(bold.Render("codestattui archive") + dim.Render(fmt.Sprintf("  %d of %d", len(m.archShown), len(m.archived))) + "\n")
	if m.query != "" || m.mode == modeSearch {
		b.WriteString(cyan.Render("/ ") + m.query)
		if m.mode == modeSearch {
			b.WriteString("█")
		}
		b.WriteString("\n")
	}
	if len(m.archShown) == 0 {
		msg := "\nNothing archived. Press a on a task to archive it."
		if len(m.archived) > 0 {
			msg = "\nNo archived tasks match."
		}
		b.WriteString(dim.Render(msg) + "\n")
	}
	// Scroll so the cursor stays visible.
	visible := max(1, m.h-5)
	start := max(0, min(m.archCursor-visible/2, len(m.archShown)-visible))
	for i := start; i < len(m.archShown) && i < start+visible; i++ {
		a := m.archShown[i]
		caret := " "
		if i == m.archCursor {
			caret = "▸"
		}
		repo := ""
		if a.Repo != "" {
			repo = " " + cyan.Render("("+a.Repo+")")
		}
		pr := ""
		if a.PR != "" {
			pr = " " + dim.Render(a.PR)
		}
		line := fmt.Sprintf("%s %s %s %s%s%s", caret, dim.Render(a.ArchivedAt.Local().Format("2006-01-02")),
			dim.Render(a.ID), a.Display(), repo, pr)
		b.WriteString(line + "\n")
	}
	if m.notice != "" {
		b.WriteString("\n" + yellow.Render(trunc(m.notice, m.w)))
	}
	if m.mode == modeSearch {
		b.WriteString("\n" + dim.Render("type to filter · enter keep · esc clear"))
	} else {
		b.WriteString("\n" + dim.Render("j/k move · / search · U restore · esc back · ? help · q quit"))
	}
	return b.String()
}

func (m Model) helpView() string {
	rows := []string{
		bold.Render("Columns"),
		"  J  jira ticket is in a ready status",
		"  C  PR CI checks",
		"  P  PR review / approval",
		"  M  PR is mergeable",
		"  D  all checks on the merge commit (after merge)",
		"  ✓  everything is ready to merge",
		"  ★  merged and ticket ready; safe to archive",
		"",
		bold.Render("Symbols"),
		"  " + icon(provider.Good) + "  ok",
		"  " + icon(provider.Pending) + "  pending",
		"  " + icon(provider.Bad) + "  failing",
		"  " + cyan.Render("◆") + "  jira ticket is ready for QA (J column)",
		"  " + icon(provider.Unknown) + "  n/a",
		"  " + red.Render("!") + "  error fetching jira or PR",
		"  " + dim.Render("…") + "  loading",
		"",
		bold.Render("Keys"),
		"  j/k or w/t  move",
		"  enter       toggle detail",
		"  u           update a field (" + updateHelp() + ")",
		"  a           archive task (moves it to archive.yaml)",
		"  A           browse archive (/ search · U restore · esc back)",
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
		icons = fmt.Sprintf("%s %s %s %s %s ", m.jiraIcon(r),
			icon(pr.CI), icon(pr.Approval), icon(pr.Merge), icon(pr.Post))
		if m.complete(r) {
			mark = green.Render("★")
		} else if m.allGood(r) {
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
	const pad = "  "
	var lines []string
	section := func(name, head string) {
		if len(lines) > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, bold.Render(name)+"  "+head)
	}
	row := func(label, v string) {
		lines = append(lines, fmt.Sprintf("%s%s %s", pad, dim.Render(fmt.Sprintf("%-8s", label)), v))
	}
	status := func(label string, s provider.State, text string) {
		lines = append(lines, fmt.Sprintf("%s%s %s %s", pad, icon(s), dim.Render(fmt.Sprintf("%-6s", label)), text))
	}
	if r.JiraKey != "" {
		if r.Jira != nil {
			section("JIRA", cyan.Render(r.JiraKey)+dim.Render(" · ")+r.Jira.Status)
			lines = append(lines, pad+r.Jira.Summary)
		} else if r.JiraErr != nil {
			section("JIRA", cyan.Render(r.JiraKey))
			lines = append(lines, pad+red.Render(r.JiraErr.Error()))
		}
	}
	if t.PR != "" {
		if r.PR != nil {
			section("PR", cyan.Render(t.PR))
			lines = append(lines, pad+r.PR.Title)
			status("review", r.PR.Approval, r.PR.ApprovalText)
			status("ci", r.PR.CI, r.PR.CIText)
			status("merge", r.PR.Merge, r.PR.MergeText)
			if r.PR.Merged {
				status("deploy", r.PR.Post, r.PR.PostText)
			}
			lines = append(lines, pad+dim.Render("branch  "+r.PR.Branch))
		} else if r.PRErr != nil {
			section("PR", cyan.Render(t.PR))
			lines = append(lines, pad+red.Render(r.PRErr.Error()))
		}
	}
	if t.Repo != "" || t.Session != "" {
		section("LOCAL", "")
		if t.Repo != "" {
			row("repo", t.Repo)
		}
		if t.Session != "" {
			row("claude", t.Session)
			row("dir", t.Dir)
		}
	}
	lines = append(lines, "", dim.Render("polled "+r.Fetched.Format("15:04:05")))
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

func Run(cfg config.Config, path, archPath string) error {
	_, err := tea.NewProgram(New(cfg, path, archPath), tea.WithAltScreen()).Run()
	return err
}
