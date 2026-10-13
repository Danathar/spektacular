package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/hivecommons/spektacular/internal/status"
)

// Watch intervals: the default, and the shortest accepted, which keeps a
// watch from re-reading every store many times a second.
const (
	defaultWatchInterval = 2 * time.Second
	minWatchInterval     = 200 * time.Millisecond
)

// watchHeaderLines is what the header takes above the tree: its line and a
// blank one.
const watchHeaderLines = 2

// Importing Bubble Tea v1 has a side effect on every command: its init
// asks the terminal for its background colour, when stdout is a terminal in
// the foreground and TERM is not screen, tmux or dumb, so that a program
// never waits for the answer after taking the terminal. A real terminal
// answers at once; a pseudo-terminal nobody is attached to makes it wait
// termenv's five-second timeout. Bubble Tea v2 drops the hook.

// watchStatus keeps frame(width) on screen in the alternate screen buffer,
// drawn again every interval, until ctx is done or the user quits with q,
// Esc or Ctrl+C. A tree taller than the screen scrolls with the arrow keys,
// j/k, the page keys, g/G for the top and bottom, and the mouse wheel; the
// scroll position survives a refresh. Each frame is rendered at the
// terminal's width, so long lines wrap to it. opts are added to the
// program's own, for a test that runs it without a terminal.
func watchStatus(ctx context.Context, term io.Writer, title string, interval time.Duration, frame func(width int) string, opts ...tea.ProgramOption) error {
	m := newWatchModel(term, title, interval, frame)
	opts = append([]tea.ProgramOption{
		tea.WithContext(ctx),
		tea.WithOutput(term),
		tea.WithAltScreen(),
		tea.WithMouseCellMotion(),
	}, opts...)
	_, err := tea.NewProgram(m, opts...).Run()
	// Both are the ways a watch is meant to stop: Ctrl+C, SIGTERM, or ctx done.
	if errors.Is(err, tea.ErrInterrupted) || errors.Is(err, tea.ErrProgramKilled) {
		return nil
	}
	return err
}

// watchModel is the watch's Bubble Tea model: the latest frame in a
// viewport under a header, refreshed on a timer.
type watchModel struct {
	term     io.Writer
	title    string
	interval time.Duration
	frame    func(width int) string

	vp            viewport.Model
	width, height int
	sized         bool // a window size has arrived
	at            time.Time
	drawn         bool // a frame has been rendered
	drawnWidth    int  // the width the frame was rendered at
	refreshing    bool // a frame is being rendered now
	resized       bool // resized while refreshing: render again when it lands
}

// frameMsg is a rendered frame, with when it was rendered and at what width.
type frameMsg struct {
	at    time.Time
	width int
	text  string
}

// tickMsg says the interval has passed since the last frame.
type tickMsg struct{}

func newWatchModel(term io.Writer, title string, interval time.Duration, frame func(width int) string) watchModel {
	vp := viewport.New(0, 0)
	vp.MouseWheelEnabled = true
	// Init renders the first frame, so a refresh is already under way.
	return watchModel{term: term, title: title, interval: interval, frame: frame, vp: vp, refreshing: true}
}

func (m watchModel) Init() tea.Cmd {
	return m.refresh()
}

// refresh renders the next frame off the update loop, so keys stay live
// while the stores are read.
func (m watchModel) refresh() tea.Cmd {
	width, frame := m.width, m.frame
	return func() tea.Msg {
		return frameMsg{at: time.Now(), width: width, text: frame(width)}
	}
}

func (m watchModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height, m.sized = msg.Width, msg.Height, true
		m.vp.Width = msg.Width
		m.vp.Height = max(0, msg.Height-watchHeaderLines)
		if m.drawn && m.drawnWidth != m.width {
			if m.refreshing {
				m.resized = true
				return m, nil
			}
			m.refreshing = true
			return m, m.refresh()
		}
		return m, nil

	case frameMsg:
		m.refreshing, m.drawn, m.drawnWidth, m.at = false, true, msg.width, msg.at
		m.vp.SetContent(strings.TrimRight(msg.text, "\n"))
		if m.resized {
			m.resized, m.refreshing = false, true
			return m, m.refresh()
		}
		return m, tea.Tick(m.interval, func(time.Time) tea.Msg { return tickMsg{} })

	case tickMsg:
		if m.refreshing {
			return m, nil
		}
		m.refreshing = true
		return m, m.refresh()

	case tea.KeyMsg:
		switch msg.String() {
		case "q", "esc", "ctrl+c":
			return m, tea.Quit
		case "g", "home":
			m.vp.GotoTop()
			return m, nil
		case "G", "end":
			m.vp.GotoBottom()
			return m, nil
		}
	}
	var cmd tea.Cmd
	m.vp, cmd = m.vp.Update(msg)
	return m, cmd
}

func (m watchModel) View() string {
	return status.WatchHeader(m.term, m.title, m.interval, m.at, m.position()) + "\n\n" + m.vp.View()
}

// position is where the view is in a tree taller than the screen, as the
// lines on screen out of the total; empty when it all fits.
func (m watchModel) position() string {
	total := m.vp.TotalLineCount()
	if !m.sized || total <= m.vp.Height {
		return ""
	}
	first := m.vp.YOffset + 1
	last := min(total, m.vp.YOffset+m.vp.Height)
	return fmt.Sprintf("%d–%d of %d", first, last, total)
}
