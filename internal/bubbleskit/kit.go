//
// [Bubbles]: https://github.com/charmbracelet/bubbles
package bubbleskit

import (
	"time"

	"charm.land/bubbles/v2/cursor"
	"charm.land/bubbles/v2/filepicker"
	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/list"
	"charm.land/bubbles/v2/paginator"
	"charm.land/bubbles/v2/progress"
	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/stopwatch"
	"charm.land/bubbles/v2/table"
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/timer"
	"charm.land/bubbles/v2/viewport"
)

type listItem struct {
	title string
	desc  string
}

func (i listItem) FilterValue() string { return i.title + " " + i.desc }
func (i listItem) Title() string       { return i.title }
func (i listItem) Description() string { return i.desc }

type Kit struct {
	Cursor     cursor.Model
	FilePicker filepicker.Model
	Help       help.Model
	List       list.Model
	Paginator  paginator.Model
	Progress   progress.Model
	Spinner    spinner.Model
	Stopwatch  stopwatch.Model
	Table      table.Model
	TextArea   textarea.Model
	TextInput  textinput.Model
	Timer      timer.Model
	Viewport   viewport.Model

	Key KeySample
}

type KeySample struct {
	Up   key.Binding
	Down key.Binding
}

func NewKit() Kit {
	const listW, listH = 40, 14

	delegate := list.NewDefaultDelegate()
	delegate.ShowDescription = false

	listItems := []list.Item{
		listItem{title: "stub-a", desc: "placeholder"},
		listItem{title: "stub-b", desc: "placeholder"},
	}

	return Kit{
		Cursor:     cursor.New(),
		FilePicker: filepicker.New(),
		Help:       help.New(),
		List:       list.New(listItems, delegate, listW, listH),
		Paginator:  paginator.New(),
		Progress:   progress.New(),
		Spinner: spinner.New(
			spinner.WithSpinner(spinner.Line),
		),
		Stopwatch: stopwatch.New(stopwatch.WithInterval(time.Second)),
		Table: table.New(
			table.WithColumns([]table.Column{
				{Title: "A", Width: 12},
				{Title: "B", Width: 12},
			}),
			table.WithRows([]table.Row{
				{"—", "—"},
			}),
		),
		TextArea:  textarea.New(),
		TextInput: textinput.New(),
		Timer:     timer.New(time.Hour, timer.WithInterval(time.Minute)),
		Viewport: viewport.New(
			viewport.WithWidth(40),
			viewport.WithHeight(12),
		),
		Key: KeySample{
			Up: key.NewBinding(
				key.WithKeys("k", "up"),
				key.WithHelp("↑/k", "up"),
			),
			Down: key.NewBinding(
				key.WithKeys("j", "down"),
				key.WithHelp("↓/j", "down"),
			),
		},
	}
}
