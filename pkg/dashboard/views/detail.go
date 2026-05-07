package views

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"oro/pkg/dashboard/data"

	"charm.land/bubbles/v2/viewport"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/glamour"
)

// Detail renders the right-panel issue details with a scrollable viewport.
type Detail struct {
	Issue          *data.Issue
	AllIssues      []data.Issue
	IssueMap       map[string]*data.Issue
	BlockingTypes  map[string]bool
	Viewport       viewport.Model
	Width          int
	Height         int
	Focused        bool
	ActiveWorkers  map[string]string // beadID -> paneID
	RichIssueID    string            // which issue has had rich detail fetched
	MetadataSchema *data.MetadataSchema
}

// NewDetail creates a detail panel.
//
//oro:testonly
func NewDetail(width, height int, issues []data.Issue) Detail {
	vp := viewport.New(viewport.WithWidth(width-2), viewport.WithHeight(height))
	return Detail{
		AllIssues: issues,
		IssueMap:  data.BuildIssueMap(issues),
		Viewport:  vp,
		Width:     width,
		Height:    height,
	}
}

// SetIssue updates the displayed issue and rebuilds content.
//
//oro:testonly
func (d *Detail) SetIssue(issue *data.Issue) {
	d.Issue = issue
	// Clear stale rich detail when switching issues
	if issue == nil || issue.ID != d.RichIssueID {
		d.RichIssueID = ""
	}
	d.Viewport.SetContent(d.renderContent())
	d.Viewport.GotoTop()
}

// SetRichDetail enriches the current issue with fields from the active issue source.
//
//oro:testonly
func (d *Detail) SetRichDetail(issueID string, rich *data.Issue) {
	d.RichIssueID = issueID
	if d.Issue != nil && d.Issue.ID == issueID && rich != nil {
		if rich.Notes != "" {
			d.Issue.Notes = rich.Notes
		}
		if rich.Design != "" {
			d.Issue.Design = rich.Design
		}
		if rich.AcceptanceCriteria != "" {
			d.Issue.AcceptanceCriteria = rich.AcceptanceCriteria
		}
		d.Viewport.SetContent(d.renderContent())
	}
}

// SetSize updates dimensions.
func (d *Detail) SetSize(width, height int) {
	d.Width = width
	d.Height = height
	d.Viewport.SetWidth(width - 2)
	d.Viewport.SetHeight(height)
	if d.Issue != nil {
		d.Viewport.SetContent(d.renderContent())
	}
}

// View renders the detail panel.
func (d *Detail) View() string {
	if d.Issue == nil {
		empty := lipgloss.NewStyle().
			Width(d.Width).
			Height(d.Height).
			Foreground(Muted).
			Align(lipgloss.Center, lipgloss.Center).
			Render("No issue selected")
		return DetailBorder.Height(d.Height).Render(empty)
	}

	content := d.Viewport.View()
	return DetailBorder.Height(d.Height).Render(content)
}

// renderMarkdown renders markdown text using glamour with dark theme.
func (d *Detail) renderMarkdown(text string) string {
	contentWidth := d.Width - 6
	if contentWidth < 20 {
		contentWidth = 20
	}
	r, err := glamour.NewTermRenderer(
		glamour.WithAutoStyle(),
		glamour.WithWordWrap(contentWidth),
	)
	if err != nil {
		return wordWrap(text, d.Width-4)
	}
	rendered, err := r.Render(text)
	if err != nil {
		return wordWrap(text, d.Width-4)
	}
	return strings.TrimRight(rendered, "\n")
}

func (d *Detail) renderContent() string {
	issue := d.Issue
	if issue == nil {
		return ""
	}

	bt := d.BlockingTypes
	if bt == nil {
		bt = data.DefaultBlockingTypes
	}

	var lines []string

	// Title
	lines = append(lines, DetailTitle.Render(issue.Title))
	lines = append(lines, "")

	// Status row
	statusSym := statusSymbol(issue, d.IssueMap, bt)
	statusLabel := paradeLabel(issue, d.IssueMap, bt)
	statusStyle := lipgloss.NewStyle().Foreground(statusColor(issue, d.IssueMap, bt))
	lines = append(lines, d.row("Status:", statusStyle.Render(statusSym+" "+statusLabel+" ("+string(issue.Status)+")")))

	// Type
	typeColor := IssueTypeColor(string(issue.IssueType))
	lines = append(lines, d.row("Type:", lipgloss.NewStyle().Foreground(typeColor).Render(string(issue.IssueType))))

	if progress, ok := d.epicProgress(issue); ok {
		progressStyle := lipgloss.NewStyle().Foreground(BrightGold).Bold(true)
		if progress.Done == progress.Total {
			progressStyle = lipgloss.NewStyle().Foreground(BrightGreen).Bold(true)
		}
		lines = append(lines, d.row("Progress:", progressStyle.Render(progress.Label())))
		lines = append(lines, "  "+moleculeProgressBar(progress.Done, progress.Total, max(d.Width-16, 10)))
	}

	// Priority
	prioColor := PriorityColor(int(issue.Priority))
	prioLabel := fmt.Sprintf("%s (%s)", data.PriorityLabel(issue.Priority), data.PriorityName(issue.Priority))
	lines = append(lines, d.row("Priority:", lipgloss.NewStyle().Foreground(prioColor).Bold(true).Render(prioLabel)))

	// Owner
	if issue.Owner != "" {
		lines = append(lines, d.row("Owner:", DetailValue.Render(issue.Owner)))
	}

	// Assignee
	if issue.Assignee != "" {
		lines = append(lines, d.row("Assignee:", DetailValue.Render(issue.Assignee)))
	}

	// Age
	lines = append(lines, d.row("Age:", DetailValue.Render(issue.AgeLabel())))

	// Due date
	if issue.DueAt != nil {
		dueLabel := issue.DueLabel()
		if issue.IsOverdue() {
			dueLabel = OverdueBadge.Render(SymOverdue + " " + dueLabel)
		} else {
			dueLabel = DueSoonBadge.Render(SymDueDate + " " + dueLabel)
		}
		lines = append(lines, d.row("Due:", dueLabel))
	}

	// Deferred
	if issue.IsDeferred() {
		lines = append(lines, d.row("Deferred:", DeferredStyle.Render(SymDeferred+" "+issue.DeferLabel())))
	}

	// ID
	lines = append(lines, d.row("ID:", DetailValue.Render(issue.ID)))

	// Worker status
	if d.ActiveWorkers != nil {
		if _, active := d.ActiveWorkers[issue.ID]; active {
			workerStyle := lipgloss.NewStyle().Foreground(StatusAgent).Bold(true)
			lines = append(lines, d.row("Worker:", workerStyle.Render(
				fmt.Sprintf("%s active", SymWorker),
			)))
		}
	}

	// Quality (HOP)
	if issue.QualityScore != nil {
		lines = append(lines, "")
		lines = append(lines, DetailSection.Render("QUALITY"))
		stars := RenderStars(*issue.QualityScore)
		scoreStr := fmt.Sprintf("%.2f (%s)", *issue.QualityScore, data.QualityLabel(*issue.QualityScore))
		lines = append(lines, d.row("Score:", stars+" "+scoreStr))

		if issue.Creator != nil {
			creatorLabel := issue.Creator.Name
			if issue.Creator.Platform != "" {
				creatorLabel += " (" + issue.Creator.Platform + ")"
			}
			lines = append(lines, d.row("Creator:", DetailValue.Render(creatorLabel)))
		}

		if len(issue.Validations) > 0 {
			lines = append(lines, d.row("Validators:", ""))
			for _, v := range issue.Validations {
				var style lipgloss.Style
				sym := SymResolved
				switch v.Outcome {
				case data.OutcomeAccepted:
					style = ValidatorAccepted
				case data.OutcomeRejected:
					style = ValidatorRejected
					sym = "✗"
				case data.OutcomeRevision:
					style = ValidatorRevision
					sym = "↻"
				}
				label := fmt.Sprintf("  %s %s %s (%.1f)",
					sym, v.Validator.Name, v.Outcome, v.QualityScore)
				lines = append(lines, style.Render(label))
			}
		}

		if issue.Crystallizes != nil {
			if *issue.Crystallizes {
				lines = append(lines, d.row("Nature:", CrystalBadge.Render(SymCrystal+" crystallizes")))
			} else {
				lines = append(lines, d.row("Nature:", EphemeralBadge.Render(SymEphemeral+" ephemeral")))
			}
		}
	}

	// Description (markdown rendered)
	if issue.Description != "" {
		lines = append(lines, "")
		lines = append(lines, DetailSection.Render("DESCRIPTION"))
		lines = append(lines, d.renderMarkdown(issue.Description))
	}

	// Metadata (from issue data, rendered against schema)
	if metaSection := d.renderMetadata(); metaSection != "" {
		lines = append(lines, "")
		lines = append(lines, metaSection)
	}

	// Close reason
	if issue.CloseReason != "" {
		lines = append(lines, "")
		lines = append(lines, DetailSection.Render("CLOSE REASON"))
		lines = append(lines, d.renderMarkdown(issue.CloseReason))
	}

	// Notes (markdown rendered)
	if issue.Notes != "" {
		lines = append(lines, "")
		lines = append(lines, DetailSection.Render("NOTES"))
		lines = append(lines, d.renderMarkdown(issue.Notes))
	}

	// Acceptance Criteria (markdown rendered)
	if issue.AcceptanceCriteria != "" {
		lines = append(lines, "")
		lines = append(lines, DetailSection.Render("ACCEPTANCE CRITERIA"))
		lines = append(lines, d.renderMarkdown(issue.AcceptanceCriteria))
	}

	// Design (markdown rendered)
	if issue.Design != "" {
		lines = append(lines, "")
		lines = append(lines, DetailSection.Render("DESIGN"))
		lines = append(lines, d.renderMarkdown(issue.Design))
	}

	// Dependencies
	eval := issue.EvaluateDependencies(d.IssueMap, bt)
	blocks := issue.BlocksIDs(d.AllIssues, bt)
	hasDeps := len(eval.Edges) > 0 || len(blocks) > 0
	if hasDeps {
		lines = append(lines, "")
		lines = append(lines, DetailSection.Render("DEPENDENCIES"))

		for _, id := range eval.BlockingIDs {
			title := id
			if dep, ok := d.IssueMap[id]; ok {
				title = dep.Title
			}
			lines = append(lines, DepBlocked.Render(
				fmt.Sprintf("  %s waiting on %s %s (%s)", SymStalled, DepArrow, id, truncate(title, 30)),
			))
		}

		for _, id := range eval.MissingIDs {
			lines = append(lines, DepMissing.Render(
				fmt.Sprintf("  %s missing %s %s (not found)", SymMissing, DepArrow, id),
			))
		}

		for _, id := range eval.ResolvedIDs {
			title := id
			if dep, ok := d.IssueMap[id]; ok {
				title = dep.Title
			}
			lines = append(lines, DepResolved.Render(
				fmt.Sprintf("  %s resolved %s %s (%s)", SymResolved, DepArrow, id, truncate(title, 30)),
			))
		}

		for _, edge := range eval.NonBlocking {
			title := edge.DependsOnID
			if dep, ok := d.IssueMap[edge.DependsOnID]; ok {
				title = dep.Title
			}
			sym, verb, style := depTypeDisplay(edge.Type)
			lines = append(lines, style.Render(
				fmt.Sprintf("  %s %s %s %s (%s)", sym, verb, DepArrow, edge.DependsOnID, truncate(title, 25)),
			))
		}

		for _, id := range blocks {
			title := id
			if dep, ok := d.IssueMap[id]; ok {
				title = dep.Title
			}
			lines = append(lines, DepBlocks.Render(
				fmt.Sprintf("  %s blocks %s %s (%s)", SymRolling, DepArrow, id, truncate(title, 30)),
			))
		}
	}

	// Cross-rig dependencies (external references)
	crossRigRefs := data.CrossRigDeps(issue)
	if len(crossRigRefs) > 0 {
		lines = append(lines, "")
		lines = append(lines, DetailSection.Render("CROSS-RIG"))
		for _, ref := range crossRigRefs {
			rigStyle := lipgloss.NewStyle().Foreground(BrightPurple).Bold(true)
			idStyle := lipgloss.NewStyle().Foreground(Light)
			lines = append(lines, fmt.Sprintf("  %s %s %s %s",
				DepArrow,
				rigStyle.Render(ref.Rig),
				idStyle.Render(ref.IssueID),
				lipgloss.NewStyle().Foreground(Dim).Render("(external)")))
		}
	}

	// Activity section (timestamps)
	lines = append(lines, "")
	lines = append(lines, d.renderActivity())

	return strings.Join(lines, "\n")
}

// renderActivity renders the activity timeline from issue timestamps.
func (d *Detail) renderActivity() string {
	issue := d.Issue
	if issue == nil {
		return ""
	}

	var lines []string
	lines = append(lines, DetailSection.Render("ACTIVITY"))

	timeStyle := lipgloss.NewStyle().Foreground(Muted)
	eventStyle := lipgloss.NewStyle().Foreground(Light)

	// Created
	lines = append(lines, fmt.Sprintf("  %s  %s",
		timeStyle.Render(formatTime(issue.CreatedAt)),
		eventStyle.Render("Created")))

	// Due date
	if issue.DueAt != nil {
		dueLabel := "Due"
		if issue.IsOverdue() {
			dueLabel = OverdueBadge.Render("Overdue")
		}
		lines = append(lines, fmt.Sprintf("  %s  %s",
			timeStyle.Render(issue.DueAt.Format("Jan 02 15:04")),
			dueLabel))
	}

	// Worker assignment
	if d.ActiveWorkers != nil && issue.ID != "" {
		if _, active := d.ActiveWorkers[issue.ID]; active {
			workerStyle := lipgloss.NewStyle().Foreground(StatusAgent)
			lines = append(lines, fmt.Sprintf("  %s  %s",
				timeStyle.Render("  now"),
				workerStyle.Render(fmt.Sprintf("%s worker active", SymWorker))))
		}
	}

	// Updated (if different from created)
	if !issue.UpdatedAt.IsZero() && issue.UpdatedAt.After(issue.CreatedAt.Add(time.Minute)) {
		lines = append(lines, fmt.Sprintf("  %s  %s",
			timeStyle.Render(formatTime(issue.UpdatedAt)),
			eventStyle.Render("Updated")))
	}

	// Closed
	if issue.ClosedAt != nil {
		lines = append(lines, fmt.Sprintf("  %s  %s",
			timeStyle.Render(formatTime(*issue.ClosedAt)),
			MolStepDone.Render("Closed")))
	}

	return strings.Join(lines, "\n")
}

// renderMetadata renders the metadata section, showing schema fields and issue values.
func (d *Detail) renderMetadata() string {
	issue := d.Issue
	schema := d.MetadataSchema

	// If no schema and no issue metadata, nothing to render
	hasMetadata := issue != nil && len(issue.Metadata) > 0
	hasSchema := schema != nil && len(schema.Fields) > 0
	if !hasSchema && !hasMetadata {
		return ""
	}

	var lines []string

	if hasSchema {
		// Render schema header with mode badge
		header := "METADATA"
		if schema.Mode != "" && schema.Mode != "none" {
			header += fmt.Sprintf(" [%s]", schema.Mode)
		}
		lines = append(lines, DetailSection.Render(header))

		fieldNames := schema.SortedFieldNames()
		for _, name := range fieldNames {
			field := schema.Fields[name]
			lines = append(lines, d.renderMetadataField(name, field, issue))
		}

		// Show any extra metadata values not in the schema
		if hasMetadata {
			extraKeys := sortedMetadataKeys(issue.Metadata, schema.Fields)
			for _, key := range extraKeys {
				val := fmt.Sprintf("%v", issue.Metadata[key])
				lines = append(lines, d.row(
					key+":",
					MetaFieldType.Render(val),
				))
			}
		}
	} else if hasMetadata {
		// No schema, but issue has metadata — show raw values
		lines = append(lines, DetailSection.Render("METADATA"))
		keys := sortedMetadataKeys(issue.Metadata, nil)
		for _, key := range keys {
			lines = append(lines, d.row(
				key+":",
				DetailValue.Render(fmt.Sprintf("%v", issue.Metadata[key])),
			))
		}
	}

	return strings.Join(lines, "\n")
}

// renderMetadataField renders a single metadata field with schema type and issue value.
func (d *Detail) renderMetadataField(fieldName string, field data.MetadataFieldSchema, issue *data.Issue) string {
	typeLabel := field.FieldTypeLabel()
	constraint := field.ConstraintLabel()

	// Build the type+constraint descriptor
	descriptor := typeLabel
	if constraint != "" {
		descriptor += " " + constraint
	}

	// Required marker
	reqMarker := ""
	if field.Required {
		reqMarker = MetaRequired.Render("*")
	}

	// Check for actual value on the issue
	valueStr := ""
	if issue != nil && issue.Metadata != nil {
		if val, ok := issue.Metadata[fieldName]; ok {
			valueStr = fmt.Sprintf("%v", val)
		}
	}

	if valueStr != "" {
		// Show: name* type = value
		return fmt.Sprintf("  %s%s %s = %s",
			MetaFieldName.Render(fieldName), reqMarker,
			MetaFieldType.Render(descriptor),
			MetaFieldValue.Render(valueStr))
	}

	// No value — show: name* type  (with dimmer style if optional)
	if field.Required {
		return fmt.Sprintf("  %s%s %s",
			MetaFieldName.Render(fieldName), reqMarker,
			MetaFieldType.Render(descriptor))
	}
	return fmt.Sprintf("  %s %s",
		MetaFieldNameDim.Render(fieldName),
		MetaFieldType.Render(descriptor))
}

// sortedMetadataKeys returns metadata keys sorted alphabetically,
// excluding any keys present in schemaFields (if non-nil).
func sortedMetadataKeys(metadata map[string]interface{}, schemaFields map[string]data.MetadataFieldSchema) []string {
	keys := make([]string, 0, len(metadata))
	for key := range metadata {
		if schemaFields != nil {
			if _, inSchema := schemaFields[key]; inSchema {
				continue
			}
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// moleculeProgressBar renders a progress bar for molecule steps with Mardi Gras gradient.
func moleculeProgressBar(done, total, width int) string {
	if total <= 0 || width <= 0 {
		return strings.Repeat(SymProgressEmpty, width)
	}
	filled := max(min(done*width/total, width), 0)
	empty := width - filled

	emptyStyle := lipgloss.NewStyle().Foreground(Dim)

	filledStr := strings.Repeat(SymProgress, filled)
	return ApplyPartialMardiGrasGradient(filledStr, width) +
		emptyStyle.Render(strings.Repeat(SymProgressEmpty, empty))
}

// formatTime renders a time as a compact label.
func formatTime(t time.Time) string {
	if t.IsZero() {
		return "          "
	}
	return t.Format("Jan 02 15:04")
}

func (d *Detail) row(label, value string) string {
	return DetailLabel.Render(label) + " " + value
}

type issueProgress struct {
	Done  int
	Total int
}

func (p issueProgress) Percent() int {
	if p.Total <= 0 {
		return 0
	}
	return p.Done * 100 / p.Total
}

func (p issueProgress) Label() string {
	return fmt.Sprintf("%d/%d (%d%%)", p.Done, p.Total, p.Percent())
}

func (d *Detail) epicProgress(issue *data.Issue) (issueProgress, bool) {
	if issue == nil || issue.IssueType != data.TypeEpic {
		return issueProgress{}, false
	}

	progress := issueProgress{}
	for _, candidate := range d.AllIssues {
		if candidate.ParentID() != issue.ID {
			continue
		}
		progress.Total++
		if candidate.Status == data.StatusClosed {
			progress.Done++
		}
	}
	if progress.Total == 0 {
		return issueProgress{}, false
	}
	return progress, true
}

func paradeLabel(issue *data.Issue, issueMap map[string]*data.Issue, blockingTypes map[string]bool) string {
	switch issue.Status {
	case data.StatusClosed:
		return "Past the Stand"
	case data.StatusInProgress:
		if issue.EvaluateDependencies(issueMap, blockingTypes).IsBlocked {
			return "Stalled"
		}
		return "Rolling"
	default:
		if issue.EvaluateDependencies(issueMap, blockingTypes).IsBlocked {
			return "Stalled"
		}
		return "Lined Up"
	}
}

func truncate(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	if maxLen <= 3 {
		return string(runes[:maxLen])
	}
	return string(runes[:maxLen-3]) + "..."
}

// depTypeDisplay returns a symbol, verb, and style for a non-blocking dependency type.
func depTypeDisplay(depType string) (symbol, verb string, style lipgloss.Style) {
	switch depType {
	case "related":
		return SymRelated, "related to", DepRelated
	case "duplicates":
		return SymDuplicates, "duplicates", DepDuplicates
	case "supersedes":
		return SymSupersedes, "supersedes", DepSupersedes
	case "discovered-from":
		return SymNonBlocking, "discovered from", DepNonBlocking
	case "waits-for":
		return SymStalled, "waits for", DepBlocked
	case "parent-child":
		return DepTree, "child of", DepNonBlocking
	case "replies-to":
		return SymNonBlocking, "replies to", DepNonBlocking
	default:
		return SymNonBlocking, depType, DepNonBlocking
	}
}

func wordWrap(s string, width int) string {
	if width <= 0 {
		return s
	}
	words := strings.Fields(s)
	if len(words) == 0 {
		return ""
	}

	var lines []string
	current := words[0]
	for _, word := range words[1:] {
		if len(current)+1+len(word) > width {
			lines = append(lines, current)
			current = word
		} else {
			current += " " + word
		}
	}
	lines = append(lines, current)
	return strings.Join(lines, "\n")
}
