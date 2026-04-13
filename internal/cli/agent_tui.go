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
	role    string
	content string
}

type agentTUIModel struct {
	client   *daemonClient
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
	titleStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12"))
	userStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("10"))
	asstStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("14"))
	metaStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	errStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
)

func newAgentTUIModel(client *daemonClient, opts agentOptions) *agentTUIModel {
	in := textarea.New()
	in.Placeholder = "Type a message and press Enter"
	in.Focus()
	in.Prompt = "> "
	in.CharLimit = 0
	in.SetHeight(3)
	in.ShowLineNumbers = false
	vp := viewport.New(0, 0)
	m := &agentTUIModel{
		client:   client,
		opts:     opts,
		input:    in,
		viewport: vp,
		lines: []chatLine{
			{role: "meta", content: "Interactive Koios agent chat. Ctrl+C or Esc to quit."},
			{role: "meta", content: fmt.Sprintf("peer=%s scope=%s", opts.Peer, opts.Scope)},
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
		m.viewport.Width = msg.Width - 2
		m.viewport.Height = max(8, msg.Height-7)
		m.input.SetWidth(max(20, msg.Width-2))
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
			m.lines = append(m.lines, chatLine{role: "user", content: content})
			m.lines = append(m.lines, chatLine{role: "assistant", content: ""})
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
			m.lines = append(m.lines, chatLine{role: "assistant", content: ""})
		}
		last := &m.lines[len(m.lines)-1]
		if last.role != "assistant" {
			m.lines = append(m.lines, chatLine{role: "assistant", content: ""})
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
		m.lines = append(m.lines, chatLine{role: "meta", content: fmt.Sprintf("session=%s attempts=%d steps=%d completed at %s", msg.SessionKey, msg.Attempts, msg.Steps, time.Now().Format(time.Kitchen))})
		m.syncViewport()
		return m, nil
	case agentErrorMsg:
		m.waiting = false
		m.err = error(msg).Error()
		m.lines = append(m.lines, chatLine{role: "error", content: m.err})
		m.syncViewport()
		return m, nil
	case agentEventMsg:
		if kind, ok := msg["kind"].(string); ok {
			m.lines = append(m.lines, chatLine{role: "meta", content: "event: " + kind})
			m.syncViewport()
		}
		return m, nil
	case agentSessionPushMsg:
		if content, ok := msg.Message["content"].(string); ok && strings.TrimSpace(content) != "" {
			m.lines = append(m.lines, chatLine{role: "meta", content: fmt.Sprintf("[%s] %s", msg.Source, content)})
			m.syncViewport()
		}
		return m, nil
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m *agentTUIModel) View() string {
	header := titleStyle.Render("Koios Agent")
	status := metaStyle.Render("Enter sends | Esc quits")
	if m.waiting {
		status = metaStyle.Render("Waiting for response...")
	}
	if m.err != "" {
		status = errStyle.Render(m.err)
	}
	return lipgloss.JoinVertical(
		lipgloss.Left,
		header,
		status,
		m.viewport.View(),
		m.input.View(),
	)
}

func (m *agentTUIModel) syncViewport() {
	var b strings.Builder
	for i, line := range m.lines {
		switch line.role {
		case "user":
			b.WriteString(userStyle.Render("you"))
		case "assistant":
			b.WriteString(asstStyle.Render("agent"))
		case "error":
			b.WriteString(errStyle.Render("error"))
		default:
			b.WriteString(metaStyle.Render("info"))
		}
		b.WriteString(": ")
		if line.role == "error" {
			b.WriteString(errStyle.Render(line.content))
		} else if line.role == "meta" {
			b.WriteString(metaStyle.Render(line.content))
		} else {
			b.WriteString(line.content)
		}
		if i < len(m.lines)-1 {
			b.WriteString("\n\n")
		}
	}
	m.viewport.SetContent(b.String())
	m.viewport.GotoBottom()
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
