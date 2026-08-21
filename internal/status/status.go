package status

import (
	"fmt"
	"os"
	"strings"
	"sync/atomic"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
)

type Event struct {
	Account string
	Mailbox string
	Message string
	Added   int
	Deleted int
}

type Reporter interface {
	Set(account, mailbox, message string)
	Added(account, mailbox string)
	Deleted(account, mailbox string)
}

type reporter struct {
	ch chan<- Event
}

func (r reporter) Set(a, m, text string) { r.ch <- Event{Account: a, Mailbox: m, Message: text} }
func (r reporter) Added(a, m string)     { r.ch <- Event{Account: a, Mailbox: m, Added: 1} }
func (r reporter) Deleted(a, m string)   { r.ch <- Event{Account: a, Mailbox: m, Deleted: 1} }

type eventMsg Event
type doneMsg struct{ err error }

type model struct {
	spinner spinner.Model
	cancel  func()
	events  <-chan Event
	done    <-chan error
	current Event
	added   int
	deleted int
}

func newModel(events <-chan Event, done <-chan error, cancel func()) model {
	s := spinner.New()
	s.Spinner = spinner.Dot
	return model{spinner: s, events: events, done: done, cancel: cancel}
}

func (m model) Init() tea.Cmd {
	return tea.Batch(m.spinner.Tick, waitEvent(m.events), waitDone(m.done))
}

func waitEvent(ch <-chan Event) tea.Cmd {
	return func() tea.Msg {
		e, ok := <-ch
		if !ok {
			return nil
		}
		return eventMsg(e)
	}
}

func waitDone(ch <-chan error) tea.Cmd {
	return func() tea.Msg { return doneMsg{err: <-ch} }
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case eventMsg:
		e := Event(msg)
		if e.Account != "" {
			m.current.Account = e.Account
		}
		if e.Mailbox != "" {
			m.current.Mailbox = e.Mailbox
		}
		if e.Message != "" {
			m.current.Message = e.Message
		}
		m.added += e.Added
		m.deleted += e.Deleted
		return m, waitEvent(m.events)
	case doneMsg:
		return m, tea.Quit
	case tea.KeyMsg:
		if msg.String() == "ctrl+c" || msg.String() == "q" {
			if m.cancel != nil {
				m.cancel()
			}
			return m, nil
		}
	}
	var cmd tea.Cmd
	m.spinner, cmd = m.spinner.Update(msg)
	return m, cmd
}

func (m model) View() string {
	where := m.current.Account
	if m.current.Mailbox != "" {
		where += "/" + m.current.Mailbox
	}
	parts := []string{m.spinner.View()}
	if where != "" {
		parts = append(parts, where)
	}
	if m.current.Message != "" {
		parts = append(parts, "—", m.current.Message)
	}
	parts = append(parts, fmt.Sprintf("(+%d/-%d)", m.added, m.deleted))
	return strings.Join(parts, " ") + "\n"
}

func Run(cancel func(), task func(Reporter) error) error {
	if !isTerminal(os.Stdout) {
		return runPlain(task)
	}
	events := make(chan Event, 128)
	uiDone := make(chan error, 1)
	result := make(chan error, 1)
	var finished atomic.Bool
	go func() {
		err := task(reporter{ch: events})
		finished.Store(true)
		result <- err
		uiDone <- err
	}()
	p := tea.NewProgram(newModel(events, uiDone, cancel))
	_, runErr := p.Run()
	if runErr != nil {
		return runErr
	}
	if !finished.Load() {
		return fmt.Errorf("status UI exited before sync completed")
	}
	return <-result
}

func runPlain(task func(Reporter) error) error {
	ch := make(chan Event, 128)
	done := make(chan error, 1)
	go func() { done <- task(reporter{ch: ch}); close(ch) }()
	for e := range ch {
		if e.Message != "" {
			where := e.Account
			if e.Mailbox != "" {
				where += "/" + e.Mailbox
			}
			fmt.Printf("[%s] %s\n", where, e.Message)
		}
	}
	return <-done
}

func isTerminal(f *os.File) bool {
	info, err := f.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}
