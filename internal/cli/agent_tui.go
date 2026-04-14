package cli

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type agentDeltaMsg string
type agentErrorMsg error
type agentDoneMsg agentRunResult
type agentEventMsg map[string]any
type agentSessionPushMsg sessionMessageEnvelope

type chatLine struct {
	at      time.Time
	role    string
	content string
}

type agentTUIModel struct {
	client   *gatewayClient
	opts     agentOptions
	program  *tea.Program
	submit   func(string)
	input    textarea.Model
	viewport viewport.Model
	lines    []chatLine
	waiting  bool
	err      string
	width    int
	height   int
}

var (
	frameStyle = lipgloss.NewStyle().
			Border(lipgloss.NormalBorder()).
			BorderForeground(lipgloss.Color("8"))
	topicBarStyle = lipgloss.NewStyle().
			Background(lipgloss.Color("24")).
			Foreground(lipgloss.Color("230")).
			Bold(true).
			Padding(0, 1)
	statusBarStyle = lipgloss.NewStyle().
			Background(lipgloss.Color("236")).
			Foreground(lipgloss.Color("252")).
			Padding(0, 1)
	inputFrameStyle = lipgloss.NewStyle().
			Border(lipgloss.NormalBorder()).
			BorderForeground(lipgloss.Color("6")).
			Padding(0, 1)
	tsStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	userStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("10"))
	asstStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("14"))
	metaStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("11"))
	errStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("9"))
	bodyStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
	headerStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("230"))
)

func newAgentTUIModel(client *gatewayClient, opts agentOptions) *agentTUIModel {
	in := textarea.New()
	in.Placeholder = "Message #koios and press Enter"
	in.Focus()
	in.Prompt = "you> "
	in.CharLimit = 0
	in.SetHeight(1)
	in.ShowLineNumbers = false
	in.FocusedStyle.CursorLine = lipgloss.NewStyle()
	in.BlurredStyle.CursorLine = lipgloss.NewStyle()
	in.FocusedStyle.Base = lipgloss.NewStyle()
	in.BlurredStyle.Base = lipgloss.NewStyle()
	vp := viewport.New(0, 0)
	m := &agentTUIModel{
		client:   client,
		opts:     opts,
		input:    in,
		viewport: vp,
		lines: []chatLine{
			{at: time.Now(), role: "meta", content: "Interactive Koios agent chat started. Ctrl+C or Esc quits."},
			{at: time.Now(), role: "meta", content: fmt.Sprintf("Connected to peer=%s scope=%s", opts.Peer, opts.Scope)},
		},
	}
	m.syncViewport()
	return m
}

func (m *agentTUIModel) Init() tea.Cmd {
	return textarea.Blink
}

func (m *agentTUIModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		contentWidth := max(20, msg.Width-4)
		m.viewport.Width = max(20, contentWidth-2)
		m.viewport.Height = max(8, msg.Height-10)
		m.input.SetWidth(max(20, contentWidth-8))
		m.syncViewport()
		return m, nil
	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyCtrlC, tea.KeyEsc:
			return m, tea.Quit
		case tea.KeyEnter:
			if m.waiting {
				return m, nil
			}
			content := strings.TrimSpace(m.input.Value())
			if content == "" {
				return m, nil
			}
			now := time.Now()
			m.lines = append(m.lines, chatLine{at: now, role: "user", content: content})
			m.lines = append(m.lines, chatLine{at: now, role: "assistant", content: ""})
			m.waiting = true
			m.err = ""
			m.input.Reset()
			m.syncViewport()
			if m.submit != nil {
				m.submit(content)
			}
			return m, nil
		}
	case agentDeltaMsg:
		if len(m.lines) == 0 {
			m.lines = append(m.lines, chatLine{at: time.Now(), role: "assistant", content: ""})
		}
		last := &m.lines[len(m.lines)-1]
		if last.role != "assistant" {
			m.lines = append(m.lines, chatLine{at: time.Now(), role: "assistant", content: ""})
			last = &m.lines[len(m.lines)-1]
		}
		last.content += string(msg)
		m.syncViewport()
		return m, nil
	case agentDoneMsg:
		m.waiting = false
		if len(m.lines) > 0 {
			last := &m.lines[len(m.lines)-1]
			if last.role == "assistant" && strings.TrimSpace(last.content) == "" {
				last.content = msg.AssistantText
			}
		}
		m.lines = append(m.lines, chatLine{
			at:      time.Now(),
			role:    "meta",
			content: fmt.Sprintf("run complete: session=%s attempts=%d steps=%d", msg.SessionKey, msg.Attempts, msg.Steps),
		})
		m.syncViewport()
		return m, nil
	case agentErrorMsg:
		m.waiting = false
		m.err = error(msg).Error()
		m.lines = append(m.lines, chatLine{at: time.Now(), role: "error", content: m.err})
		m.syncViewport()
		return m, nil
	case agentEventMsg:
		if kind, ok := msg["kind"].(string); ok {
			m.lines = append(m.lines, chatLine{at: time.Now(), role: "meta", content: "event: " + kind})
			m.syncViewport()
		}
		return m, nil
	case agentSessionPushMsg:
		if content, ok := msg.Message["content"].(string); ok && strings.TrimSpace(content) != "" {
			m.lines = append(m.lines, chatLine{at: time.Now(), role: "meta", content: fmt.Sprintf("[%s] %s", msg.Source, content)})
			m.syncViewport()
		}
		return m, nil
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m *agentTUIModel) View() string {
	contentWidth := max(20, m.width-4)
	headerLeft := headerStyle.Render(fmt.Sprintf("#koios  peer:%s", m.opts.Peer))
	headerRight := metaStyle.Render(fmt.Sprintf("scope:%s", m.opts.Scope))
	header := topicBarStyle.Width(contentWidth).Render(lipgloss.JoinHorizontal(
		lipgloss.Left,
		headerLeft,
		strings.Repeat(" ", max(1, contentWidth-lipgloss.Width(headerLeft)-lipgloss.Width(headerRight)-2)),
		headerRight,
	))

	status := "Enter sends | Esc quits"
	if m.waiting {
		status = "Awaiting agent reply..."
	}
	if m.err != "" {
		status = m.err
	}
	statusStyle := statusBarStyle
	if m.err != "" {
		statusStyle = statusStyle.Foreground(lipgloss.Color("9"))
	}
	statusBar := statusStyle.Width(contentWidth).Render(status)

	transcript := frameStyle.Width(contentWidth).Height(max(8, m.viewport.Height+2)).Render(m.viewport.View())
	inputLabel := metaStyle.Render("input")
	inputPane := inputFrameStyle.Width(contentWidth).Render(
		lipgloss.JoinVertical(
			lipgloss.Left,
			inputLabel,
			m.input.View(),
		),
	)

	return lipgloss.JoinVertical(
		lipgloss.Left,
		header,
		statusBar,
		transcript,
		inputPane,
	)
}

func (m *agentTUIModel) syncViewport() {
	var b strings.Builder
	for i, line := range m.lines {
		b.WriteString(tsStyle.Render(line.at.Format("15:04")))
		b.WriteString(" ")
		nick, content := m.renderLine(line)
		b.WriteString(nick)
		if line.role == "assistant" {
			b.WriteString(" ")
		} else {
			b.WriteString("  ")
		}
		b.WriteString(content)
		if i < len(m.lines)-1 {
			b.WriteString("\n")
		}
	}
	m.viewport.SetContent(b.String())
	m.viewport.GotoBottom()
}

func (m *agentTUIModel) renderLine(line chatLine) (string, string) {
	switch line.role {
	case "user":
		return userStyle.Render("<you>"), bodyStyle.Render(line.content)
	case "assistant":
		return asstStyle.Render("<agent>"), bodyStyle.Render(line.content)
	case "error":
		return errStyle.Render("-!-"), errStyle.Render(line.content)
	default:
		return metaStyle.Render("-*-"), metaStyle.Render(line.content)
	}
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
