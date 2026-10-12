package status

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/term"
)

// Bar widths in cells: the epic's, over every task, and each spec's own.
const (
	barWidth     = 20
	specBarWidth = 10
)

// Column gaps and the narrowest column worth wrapping or truncating into:
// below that, a line is left to the terminal.
const (
	gap      = "   "
	minWrap  = 16
	ellipsis = "…"
)

// theme is the readable tree's styles. Its renderer decides, from the
// terminal writer, whether colour is used at all: a writer that is not a terminal,
// or NO_COLOR, gets the same text with no escape codes.
type theme struct {
	title, heading, dim, bold, accent, ok, warn, bad, info lipgloss.Style
}

func newTheme(w io.Writer) theme {
	r := lipgloss.NewRenderer(w)
	// The 16 ANSI colours follow the terminal's own theme, light or dark,
	// without querying the terminal for its background.
	return theme{
		title:   r.NewStyle().Bold(true).Foreground(lipgloss.Color("5")),
		heading: r.NewStyle().Bold(true),
		dim:     r.NewStyle().Faint(true),
		bold:    r.NewStyle().Bold(true),
		accent:  r.NewStyle().Foreground(lipgloss.Color("6")),
		ok:      r.NewStyle().Foreground(lipgloss.Color("2")),
		warn:    r.NewStyle().Foreground(lipgloss.Color("3")),
		bad:     r.NewStyle().Foreground(lipgloss.Color("1")),
		info:    r.NewStyle().Foreground(lipgloss.Color("4")),
	}
}

// stateStyle is a spec state's glyph and colour.
func (t theme) stateStyle(state SpecState) (string, lipgloss.Style) {
	switch state {
	case StateImplemented:
		return "✓", t.ok
	case StateInProgress:
		return "◐", t.warn
	case StatePlanned:
		return "○", t.info
	case StateStale:
		return "!", t.bad
	case StateMissing:
		return "✗", t.bad
	default:
		return "·", t.dim
	}
}

// pad renders text in style and pads it, outside the style, to width cells.
func pad(style lipgloss.Style, text string, width int) string {
	return style.Render(text) + strings.Repeat(" ", max(0, width-lipgloss.Width(text)))
}

// bar is a progress bar of done out of total, width cells wide, filled in
// style.
func (t theme) bar(done, total, width int, style lipgloss.Style) string {
	filled := 0
	if total > 0 {
		filled = done * width / total
	}
	return style.Render(strings.Repeat("━", filled)) + t.dim.Render(strings.Repeat("─", width-filled))
}

// PrettyOptions shapes the readable tree.
type PrettyOptions struct {
	// Term is the writer whose terminal decides whether the tree is
	// coloured, for an output writer that wraps the terminal, such as one
	// that also copies the output into a log. Nil means the output writer.
	Term io.Writer
	// Width is the terminal width in cells, which long lines wrap or
	// truncate to. 0 means Term's width when it is a terminal, and no
	// wrapping otherwise.
	Width int
}

// RenderPretty writes the readable tree: every workflow in progress when
// there is one, the epic's header with its status, progress bar and
// roll-up, then one line per spec with its state and task counts. The
// requested spec is highlighted and expanded to its milestones and tasks.
// When no spec is requested, as when the epic itself is named or an epic
// orchestrator's run is reported, every spec being implemented is expanded
// instead. Other specs show one line each, plus their dependencies. A
// standalone spec has no epic header.
func RenderPretty(w io.Writer, r Report) error {
	return RenderPrettyWith(w, r, PrettyOptions{})
}

// RenderPrettyStyled is RenderPretty, with colour decided by whether term,
// rather than w, is a terminal.
func RenderPrettyStyled(w io.Writer, r Report, term io.Writer) error {
	return RenderPrettyWith(w, r, PrettyOptions{Term: term})
}

// RenderPrettyWith is RenderPretty shaped by o.
func RenderPrettyWith(w io.Writer, r Report, o PrettyOptions) error {
	if o.Term == nil {
		o.Term = w
	}
	if o.Width == 0 {
		o.Width = terminalWidth(o.Term)
	}
	p := prettyWriter{theme: newTheme(o.Term), width: o.Width}
	p.report(r)
	_, err := io.WriteString(w, p.String())
	return err
}

// terminalWidth is w's width in cells when it is a terminal, else 0.
func terminalWidth(w io.Writer) int {
	f, ok := w.(*os.File)
	if !ok {
		return 0
	}
	width, _, err := term.GetSize(f.Fd())
	if err != nil {
		return 0
	}
	return width
}

// prettyWriter builds the readable tree line by line.
type prettyWriter struct {
	strings.Builder
	theme
	width int
}

// line writes one line, with any padding at its end dropped.
func (p *prettyWriter) line(text string) {
	p.WriteString(strings.TrimRight(text, " ") + "\n")
}

// note writes a dim "label: text" line under indent, wrapped to the width
// with the continuation lines hanging under the text.
func (p *prettyWriter) note(indent, label, text string) {
	label += ": "
	hang := indent + strings.Repeat(" ", lipgloss.Width(label))
	lines := wrap(text, p.width-lipgloss.Width(hang))
	p.line(indent + p.dim.Render(label+lines[0]))
	for _, l := range lines[1:] {
		p.line(hang + p.dim.Render(l))
	}
}

// report writes the whole tree for r.
func (p *prettyWriter) report(r Report) {
	workflows := r.Workflows
	if len(workflows) == 0 && r.Workflow != nil {
		workflows = []WorkflowInfo{*r.Workflow}
	}
	if len(workflows) > 0 {
		p.workflows(workflows)
		if r.Specs != nil {
			p.WriteString("\n")
		}
	} else if r.Specs == nil {
		p.line(p.dim.Render("no workflow in progress"))
	}

	indent := ""
	if r.Epic != nil {
		p.epic(r.Epic)
		p.WriteString("\n")
		indent = "  "
		if len(r.Specs) == 0 {
			p.line(indent + p.dim.Render("no specs"))
		}
	}

	nameW, stateW, countW, bars := 0, 0, 0, false
	counts := make([]string, len(r.Specs))
	for i, s := range r.Specs {
		counts[i] = countText(s, planningStep(r, s))
		nameW = max(nameW, lipgloss.Width(s.Name))
		stateW = max(stateW, lipgloss.Width(Label(s.State)))
		countW = max(countW, lipgloss.Width(counts[i]))
		bars = bars || (s.Plan != nil && s.Plan.Progress != nil)
	}

	requested := requestedIndex(r)
	child := indent + "    "
	for i, s := range r.Specs {
		glyph, style := p.stateStyle(s.State)
		nameStyle := p.bold
		if i == requested {
			nameStyle = p.accent.Bold(true)
		}
		cols := []string{indent + style.Render(glyph), pad(nameStyle, s.Name, nameW), pad(style, Label(s.State), stateW)}
		if bars {
			bar := strings.Repeat(" ", specBarWidth)
			if s.Plan != nil && s.Plan.Progress != nil {
				bar = p.bar(s.Plan.Progress.TasksCompleted, s.Plan.Progress.TasksTotal, specBarWidth, style)
			}
			cols = append(cols, bar)
		}
		cols = append(cols, pad(lipgloss.NewStyle(), counts[i], countW))
		p.line(strings.Join(cols[:2], " ") + gap + strings.Join(cols[2:], gap))
		if len(s.DependsOn) > 0 {
			p.note(child, "depends on", strings.Join(s.DependsOn, ", "))
		}
		if expands(r, requested, i, s) && s.Plan != nil && len(s.Plan.Tasks) > 0 {
			p.tasks(s.Plan.Tasks, child)
		}
	}
}

// workflows lists every workflow in progress, one aligned line each.
func (p *prettyWriter) workflows(workflows []WorkflowInfo) {
	title := "workflow in progress"
	if len(workflows) > 1 {
		title = fmt.Sprintf("%d workflows in progress", len(workflows))
	}
	p.line(p.title.Render(title))
	kindW, nameW := 0, 0
	for _, wf := range workflows {
		kindW = max(kindW, lipgloss.Width(wf.Kind))
		nameW = max(nameW, lipgloss.Width(wf.Name))
	}
	for _, wf := range workflows {
		line := fmt.Sprintf("  %s %s  %s  %s %s", p.warn.Render("●"), pad(p.info, wf.Kind, kindW),
			pad(p.bold, wf.Name, nameW), p.dim.Render("at step"), wf.CurrentStep)
		if wf.Orchestrated {
			line += "  " + p.dim.Render("(orchestrated)")
		}
		p.line(line)
	}
}

// epic writes the epic's header: its name and status, a progress bar over
// its tasks, and the planning and implementing roll-up when present.
func (p *prettyWriter) epic(e *EpicStatus) {
	docStatus := e.DocumentStatus
	if docStatus == "" {
		docStatus = "no status"
	}
	header := p.title.Render("epic "+e.Name) + "  " + p.dim.Render("("+docStatus+")")
	if e.Done {
		header += "  " + p.ok.Render("✓ done")
	}
	p.line(header)

	barStyle := p.warn
	if e.Done {
		barStyle = p.ok
	}
	sep := p.dim.Render(" · ")
	p.line(fmt.Sprintf("  %s  %d/%d tasks%s%d/%d specs planned%s%d/%d implemented",
		p.bar(e.Progress.TasksCompleted, e.Progress.TasksTotal, barWidth, barStyle),
		e.Progress.TasksCompleted, e.Progress.TasksTotal, sep,
		e.Progress.SpecsPlanned, e.Progress.SpecsTotal, sep,
		e.Progress.SpecsImplemented, e.Progress.SpecsTotal))
	if e.Run != nil {
		labelW := lipgloss.Width("implementing")
		p.line("  " + pad(p.heading, "planning", labelW) + "  " + runCountsText(e.Run.Plan))
		p.line("  " + pad(p.heading, "implementing", labelW) + "  " + runCountsText(e.Run.Implement))
		for _, problem := range e.Run.Problems {
			p.line("  " + p.bad.Render("problem ("+problem.Code+"):") + " " + problem.Message)
		}
	}
}

// tasks writes a spec's tasks under their milestones, each milestone with
// its count of tasks done, and each task with its checkbox, repo and
// execution, then the reason a human does it and the titles of the tasks
// it depends on. When the columns would not fit the width, the titles are
// cut to fit rather than left to the terminal to wrap.
func (p *prettyWriter) tasks(tasks []TaskStatus, indent string) {
	titleW, repoW := 0, 0
	titles := make(map[string]string, len(tasks))
	done := map[int]int{}
	total := map[int]int{}
	for _, task := range tasks {
		titleW = max(titleW, lipgloss.Width(task.Title))
		repoW = max(repoW, lipgloss.Width(task.Repo.Name))
		titles[task.ID] = task.Title
		total[task.Milestone]++
		if task.Completed {
			done[task.Milestone]++
		}
	}
	// The row is: indent, two spaces, box, space, title, gap, repo, gap,
	// execution.
	fixed := lipgloss.Width(indent) + 6 + repoW + 2*len(gap) + lipgloss.Width("agent")
	if p.width > 0 && fixed+titleW > p.width && p.width-fixed >= minWrap {
		titleW = p.width - fixed
	}

	milestone := -1
	for _, task := range tasks {
		if task.Milestone != milestone {
			milestone = task.Milestone
			p.line(fmt.Sprintf("%s%s  %s", indent, p.heading.Render(fmt.Sprintf("Milestone %d", milestone)),
				p.dim.Render(fmt.Sprintf("%d/%d", done[milestone], total[milestone]))))
		}
		box, boxStyle, titleStyle := "[ ]", p.dim, lipgloss.NewStyle()
		if task.Completed {
			box, boxStyle, titleStyle = "[x]", p.ok, p.dim
		}
		execStyle := p.dim
		if task.Execution.Type == "human" {
			execStyle = p.warn
		}
		title := cut(task.Title, titleW)
		p.line(fmt.Sprintf("%s  %s %s%s%s%s%s", indent, boxStyle.Render(box), pad(titleStyle, title, titleW),
			gap, pad(p.info, task.Repo.Name, repoW), gap, execStyle.Render(task.Execution.Type)))
		sub := indent + "      "
		if task.Execution.Reason != "" {
			p.note(sub, "reason", task.Execution.Reason)
		}
		if len(task.DependsOn) > 0 {
			names := make([]string, len(task.DependsOn))
			for i, id := range task.DependsOn {
				names[i] = titles[id]
				if names[i] == "" {
					names[i] = id
				}
			}
			p.note(sub, "depends on", strings.Join(names, ", "))
		}
	}
}

// cut shortens text to width cells, ending it with an ellipsis when it
// was longer.
func cut(text string, width int) string {
	if lipgloss.Width(text) <= width {
		return text
	}
	return strings.TrimRight(ansi.Truncate(text, width-lipgloss.Width(ellipsis), ""), " ") + ellipsis
}

// wrap breaks text into lines of at most width cells at spaces. A width
// below minWrap, as when the terminal is too narrow or unknown, leaves the
// text as one line, and a word longer than the width is a line of its own.
func wrap(text string, width int) []string {
	if width < minWrap {
		return []string{text}
	}
	var lines []string
	var line string
	for _, word := range strings.Fields(text) {
		switch {
		case line == "":
			line = word
		case lipgloss.Width(line)+1+lipgloss.Width(word) <= width:
			line += " " + word
		default:
			lines = append(lines, line)
			line = word
		}
	}
	if line != "" || len(lines) == 0 {
		lines = append(lines, line)
	}
	return lines
}

// countText is a spec's progress column: its plan's task counts, phase
// counts for an older plan, the step its plan is being written at, or why
// there are none. planning is that step, empty when no plan is being written.
func countText(s SpecStatus, planning string) string {
	switch {
	case s.Plan == nil && planning != "":
		return "planning · " + planning
	case s.Plan == nil:
		return "no plan"
	case s.Plan.Progress != nil:
		return fmt.Sprintf("%d/%d tasks", s.Plan.Progress.TasksCompleted, s.Plan.Progress.TasksTotal)
	case s.legacy:
		return fmt.Sprintf("%d/%d phases", s.counts.Completed, s.counts.Total)
	default:
		return "no tasks"
	}
}

// planningStep is the step the spec's plan workflow is at, from its run view
// or, without one, from the workflows in progress; empty when no plan for it
// is being written.
func planningStep(r Report, s SpecStatus) string {
	if s.Run != nil && s.Run.Plan.State == "in_progress" {
		return s.Run.Plan.CurrentStep
	}
	for _, wf := range r.Workflows {
		if wf.Kind == "plan" && wf.Name == s.Name {
			return wf.CurrentStep
		}
	}
	return ""
}

// expands reports whether spec i's milestones and tasks are listed: the
// requested spec always is, and when no spec is requested so is every spec
// being implemented, by its task counts, its implement run, or an implement
// workflow in progress for it.
func expands(r Report, requested, i int, s SpecStatus) bool {
	if requested >= 0 {
		return i == requested
	}
	if s.State == StateInProgress {
		return true
	}
	if s.Run != nil && s.Run.Implement.State == "in_progress" {
		return true
	}
	for _, wf := range r.Workflows {
		if wf.Kind == "implement" && wf.Name == s.Name {
			return true
		}
	}
	return false
}

// requestedIndex is the spec the report was asked about: the one whose name,
// or whose plan's name, is the requested name. It is highlighted and its tasks
// listed. -1 when the request named the epic itself, even when a spec shares
// the epic's name, or named nothing; the specs being implemented are expanded
// then instead.
func requestedIndex(r Report) int {
	if r.Requested == "" || (r.Epic != nil && r.Requested == r.Epic.Name) {
		return -1
	}
	for i, s := range r.Specs {
		if s.Name == r.Requested || (s.Plan != nil && s.Plan.Name == r.Requested) {
			return i
		}
	}
	return -1
}

// runCountsText words one part's counts for the epic header, for example
// "1 done, 1 in progress, 2 ready, 0 blocked".
func runCountsText(c RunCounts) string {
	text := fmt.Sprintf("%d done, %d in progress", c.Done, c.InProgress)
	if c.AwaitingMerge > 0 {
		text += fmt.Sprintf(", %d awaiting merge", c.AwaitingMerge)
	}
	return text + fmt.Sprintf(", %d ready, %d blocked", c.Ready, c.Blocked)
}

// WatchHeader is the line above each frame of a watch: what is watched, how
// often it refreshes, when this frame was drawn, where the view is when the
// tree is taller than the screen (position, empty otherwise), and the keys.
func WatchHeader(term io.Writer, title string, interval time.Duration, at time.Time, position string) string {
	t := newTheme(term)
	parts := []string{"every " + interval.String(), at.Format("15:04:05")}
	if position != "" {
		parts = append(parts, position, "↑↓ scroll")
	}
	parts = append(parts, "q quit")
	return t.title.Render(title) + "  " + t.dim.Render(strings.Join(parts, " · "))
}

// WatchError is a refusal shown in a watch frame instead of the tree, so a
// watch survives a document that is briefly missing or mid-write.
func WatchError(term io.Writer, message string) string {
	return newTheme(term).bad.Render("✗ " + message)
}
