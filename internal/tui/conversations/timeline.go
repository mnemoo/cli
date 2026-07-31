package conversations

import (
	"fmt"
	"strings"

	"github.com/mnemoo/cli/internal/stake"
)

type timelineBlockKind int

const (
	timelineMessage timelineBlockKind = iota
	timelineSystem
	timelineStack
)

type timelineBlock struct {
	key    string
	kind   timelineBlockKind
	events []stake.ApprovalEvent
}

func buildTimelineBlocks(events []stake.ApprovalEvent) []timelineBlock {
	blocks := make([]timelineBlock, 0, len(events))
	for index := 0; index < len(events); {
		event := events[index]
		if isStackableEvent(event) {
			end := index + 1
			for end < len(events) && isStackableEvent(events[end]) {
				end++
			}
			group := append([]stake.ApprovalEvent(nil), events[index:end]...)
			blocks = append(blocks, timelineBlock{
				key:    timelineKey("stack", group),
				kind:   timelineStack,
				events: group,
			})
			index = end
			continue
		}
		kind := timelineSystem
		if isTimelineMessage(event) {
			kind = timelineMessage
		}
		blocks = append(blocks, timelineBlock{
			key:    timelineKey("event", []stake.ApprovalEvent{event}),
			kind:   kind,
			events: []stake.ApprovalEvent{event},
		})
		index++
	}
	return blocks
}

func isTimelineMessage(event stake.ApprovalEvent) bool {
	if event.IsMessage() {
		return true
	}
	return event.Data.Comment != nil && strings.TrimSpace(*event.Data.Comment) != ""
}

func isStackableEvent(event stake.ApprovalEvent) bool {
	switch event.Type {
	case "version_publish", "version_change", "guideline_checked", "guideline_unchecked":
		return true
	default:
		return false
	}
}

func timelineKey(prefix string, events []stake.ApprovalEvent) string {
	if len(events) == 0 {
		return prefix
	}
	if prefix == "stack" {
		return fmt.Sprintf("%s:%d", prefix, events[0].ID)
	}
	return fmt.Sprintf("%s:%d:%d", prefix, events[0].ID, events[len(events)-1].ID)
}

func (m *Model) renderTimelineContent() {
	var builder strings.Builder
	m.timelineOffsets = make([]int, 0, len(m.timelineBlocks))
	line := 0
	width := max(30, m.viewport.Width()-4)
	for index, block := range m.timelineBlocks {
		m.timelineOffsets = append(m.timelineOffsets, line)
		rendered := renderTimelineBlock(
			block,
			index == m.timelineCursor,
			m.expandedGroups[block.key],
			width,
		)
		builder.WriteString(rendered)
		line += strings.Count(rendered, "\n") + 1
		if index < len(m.timelineBlocks)-1 {
			builder.WriteString("\n\n")
			line += 2
		}
	}
	m.viewport.SetContent(builder.String())
}

func (m *Model) moveTimelineCursor(delta int) {
	m.setTimelineCursor(m.timelineCursor + delta)
}

func (m *Model) setTimelineCursor(index int) {
	if len(m.timelineBlocks) == 0 {
		return
	}
	index = max(0, min(index, len(m.timelineBlocks)-1))
	m.timelineCursor = index
	m.renderTimelineContent()
	if index == len(m.timelineBlocks)-1 {
		m.viewport.GotoBottom()
		return
	}
	if index < len(m.timelineOffsets) {
		m.viewport.SetYOffset(m.timelineOffsets[index])
	}
}

func (m *Model) jumpToMessage(direction int) {
	if direction == 0 {
		return
	}
	for index := m.timelineCursor + direction; index >= 0 && index < len(m.timelineBlocks); index += direction {
		if m.timelineBlocks[index].kind == timelineMessage {
			m.setTimelineCursor(index)
			return
		}
	}
}

func (m *Model) toggleTimelineGroup() {
	if m.timelineCursor < 0 || m.timelineCursor >= len(m.timelineBlocks) {
		return
	}
	block := m.timelineBlocks[m.timelineCursor]
	if block.kind != timelineStack {
		return
	}
	m.expandedGroups[block.key] = !m.expandedGroups[block.key]
	m.renderTimelineContent()
	if m.timelineCursor < len(m.timelineOffsets) {
		m.viewport.SetYOffset(m.timelineOffsets[m.timelineCursor])
	}
}

func renderTimelineBlock(block timelineBlock, selected, expanded bool, width int) string {
	if len(block.events) == 0 {
		return ""
	}
	switch block.kind {
	case timelineMessage:
		return renderMessage(block.events[0], selected, width)
	case timelineStack:
		return renderEventStack(block, selected, expanded)
	default:
		return renderSystemEvent(block.events[0], selected)
	}
}

func renderMessage(event stake.ApprovalEvent, selected bool, width int) string {
	prefix := "  "
	if selected {
		prefix = "› "
	}
	author := strings.TrimSpace(event.User.Name)
	if author == "" {
		author = "System"
	}
	labels := make([]string, 0, 2)
	if event.IsPrivate() {
		labels = append(labels, "PRIVATE")
	}
	switch event.Type {
	case "approval":
		labels = append(labels, "APPROVAL NOTE")
	case "status_change":
		labels = append(labels, "STATUS NOTE")
	case "rating_submission":
		labels = append(labels, "RATING NOTE")
	}
	header := fmt.Sprintf("%s● %s • %s", prefix, author, formatEventTime(event.Time))
	if len(labels) > 0 {
		header += " • " + strings.Join(labels, " • ")
	}
	lines := []string{header}
	if event.Data.Comment != nil && strings.TrimSpace(*event.Data.Comment) != "" {
		for _, line := range wrapText(*event.Data.Comment, max(20, width-5)) {
			lines = append(lines, "    "+line)
		}
	}
	if count := len(event.Data.Images); count > 0 {
		label := "attachment"
		if count != 1 {
			label = "attachments"
		}
		lines = append(lines, fmt.Sprintf("    ↳ %d %s", count, label))
	}
	return strings.Join(lines, "\n")
}

func renderEventStack(block timelineBlock, selected, expanded bool) string {
	prefix := "  "
	if selected {
		prefix = "› "
	}
	marker := "▸"
	if expanded {
		marker = "▾"
	}
	last := block.events[len(block.events)-1]
	header := fmt.Sprintf("%s%s %s", prefix, marker, eventAction(last))
	if len(block.events) > 1 {
		header += fmt.Sprintf(" • +%d more", len(block.events)-1)
	}
	header += " • " + formatEventTime(last.Time)
	if !expanded {
		return header
	}
	lines := []string{header}
	for index, event := range block.events {
		branch := "├"
		if index == len(block.events)-1 {
			branch = "└"
		}
		lines = append(lines, fmt.Sprintf("    %s %s • %s", branch, eventAction(event), formatEventTime(event.Time)))
	}
	return strings.Join(lines, "\n")
}

func renderSystemEvent(event stake.ApprovalEvent, selected bool) string {
	prefix := "  "
	if selected {
		prefix = "› "
	}
	return fmt.Sprintf("%s─ %s • %s", prefix, eventAction(event), formatEventTime(event.Time))
}

func eventAction(event stake.ApprovalEvent) string {
	author := strings.TrimSpace(event.User.Name)
	if author == "" {
		author = "System"
	}
	summary := systemEventSummary(event)
	if summary == "" {
		summary = strings.ReplaceAll(event.Type, "_", " ")
	}
	return author + " — " + compact(summary, 72)
}
