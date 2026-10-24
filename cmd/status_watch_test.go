package cmd

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/require"
)

// A watch renders a frame at once and again after every interval, and
// stops cleanly when its context is done.
func TestWatchStatus_RedrawsUntilStopped(t *testing.T) {
	var (
		mu    sync.Mutex
		calls int
	)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	frame := func(int) string {
		mu.Lock()
		defer mu.Unlock()
		calls++
		if calls == 3 {
			cancel()
		}
		return "frame"
	}

	var out bytes.Buffer
	require.NoError(t, watchStatus(ctx, &out, "title", time.Millisecond, frame,
		tea.WithInput(nil), tea.WithoutRenderer(), tea.WithoutSignalHandler()))
	mu.Lock()
	defer mu.Unlock()
	require.GreaterOrEqual(t, calls, 3)
}

// update applies msg to m and, when that started a refresh, runs it and
// feeds its frame back in. A tick it schedules is left to wait.
func update(t *testing.T, m watchModel, msg tea.Msg) watchModel {
	t.Helper()
	next, cmd := m.Update(msg)
	m = next.(watchModel)
	if cmd == nil || !m.refreshing {
		return m
	}
	got, ok := cmd().(frameMsg)
	require.True(t, ok, "a refresh renders a frame")
	return update(t, m, got)
}

// lines is a frame of n numbered lines.
func lines(n int) string {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "line %d\n", i)
	}
	return b.String()
}

// Frames are rendered at the terminal's width, the viewport takes the
// screen below the header, and a tree taller than the screen scrolls with
// the keys, keeping its place across a refresh.
func TestWatchModel_ScrollsAndRefreshes(t *testing.T) {
	var widths []int
	m := newWatchModel(&bytes.Buffer{}, "title", time.Minute, func(width int) string {
		widths = append(widths, width)
		return lines(30)
	})

	m = update(t, m, m.Init()())
	require.Equal(t, []int{0}, widths, "the first frame is drawn before the size is known")
	require.Equal(t, "", m.position(), "no position without a screen to fit")

	m = update(t, m, tea.WindowSizeMsg{Width: 80, Height: 12})
	require.Equal(t, []int{0, 80}, widths, "a new width draws the frame again")
	require.Equal(t, 10, m.vp.Height)
	require.Equal(t, "1–10 of 30", m.position())
	view := m.View()
	require.Contains(t, view, "title  every 1m0s · ")
	require.Contains(t, view, "1–10 of 30 · ↑↓ scroll · q quit")
	require.Contains(t, view, "\nline 1 ")
	require.NotContains(t, view, "line 11")

	m = update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
	require.Equal(t, "2–11 of 30", m.position())
	m = update(t, m, tea.KeyMsg{Type: tea.KeyPgDown})
	require.Equal(t, "12–21 of 30", m.position())
	m = update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("G")})
	require.Equal(t, "21–30 of 30", m.position())
	m = update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("k")})
	require.Equal(t, "20–29 of 30", m.position())

	m = update(t, m, tickMsg{})
	require.Equal(t, []int{0, 80, 80}, widths, "a tick draws the frame again")
	require.Equal(t, "20–29 of 30", m.position(), "a refresh keeps the scroll position")

	m = update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("g")})
	require.Equal(t, "1–10 of 30", m.position())

	m = update(t, m, tea.WindowSizeMsg{Width: 80, Height: 40})
	require.Equal(t, []int{0, 80, 80}, widths, "the same width is not drawn again")
	require.Equal(t, "", m.position(), "a tree that fits has no position")
	require.NotContains(t, m.View(), "scroll")

	for _, key := range []string{"q", "esc", "ctrl+c"} {
		msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)}
		switch key {
		case "esc":
			msg = tea.KeyMsg{Type: tea.KeyEsc}
		case "ctrl+c":
			msg = tea.KeyMsg{Type: tea.KeyCtrlC}
		}
		_, cmd := m.Update(msg)
		require.NotNil(t, cmd, key)
		require.IsType(t, tea.QuitMsg{}, cmd(), key)
	}
}

// A resize that lands while a frame is being drawn is honoured once that
// frame lands, by drawing again at the new width.
func TestWatchModel_ResizeWhileRefreshing(t *testing.T) {
	var widths []int
	m := newWatchModel(&bytes.Buffer{}, "title", time.Minute, func(width int) string {
		widths = append(widths, width)
		return "frame"
	})
	m = update(t, m, m.Init()())
	m = update(t, m, tea.WindowSizeMsg{Width: 80, Height: 12})
	require.Equal(t, []int{0, 80}, widths)

	next, cmd := m.Update(tickMsg{})
	m = next.(watchModel)
	require.True(t, m.refreshing)
	next, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 12})
	m = next.(watchModel)
	require.True(t, m.resized)

	m = update(t, m, cmd())
	require.Equal(t, []int{0, 80, 80, 100}, widths)
	require.False(t, m.resized)
	require.Equal(t, 100, m.drawnWidth)
}

func TestStatus_WatchRefusals(t *testing.T) {
	stProject(t)

	stdout, _, code := runRootCmd(t, "status", "--watch", "--format", "json")
	require.Equal(t, 1, code)
	er := decodeError(t, stdout)
	require.Equal(t, "status_watch_format_unsupported", er.Code)
	require.Contains(t, er.NextAction, "--watch")

	stdout, _, code = runRootCmd(t, "status", "--watch", "--interval", "10ms")
	require.Equal(t, 1, code)
	er = decodeError(t, stdout)
	require.Equal(t, "status_interval_invalid", er.Code)
	require.Contains(t, er.NextAction, "200ms")
}
