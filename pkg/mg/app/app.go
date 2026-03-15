package app

import (
	"context"
	"fmt"
	"os/exec"
	"time"

	"oro/pkg/mg"
	"oro/pkg/mg/components"
	"oro/pkg/mg/data"
	"oro/pkg/mg/ui"
	"oro/pkg/mg/views"

	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/atotto/clipboard"
)

// Pane tracks which panel is focused.
type Pane int

const (
	PaneParade Pane = iota
	PaneDetail
)

// LayoutPreset defines panel arrangement modes.
type LayoutPreset int

const (
	LayoutDefault LayoutPreset = iota // parade + detail (2:3 split)
	LayoutWide                        // full-width parade only
	layoutPresetCount
)

const (
	toastDuration           = 4 * time.Second
	changeIndicatorDuration = 30 * time.Second
)

// Model is the root BubbleTea model.
type Model struct {
	issues        []data.Issue
	groups        map[data.ParadeStatus][]data.Issue
	parade        views.Parade
	detail        views.Detail
	header        components.Header
	activPane     Pane
	width         int
	height        int
	watchPath     string
	pathExplicit  bool
	lastFileMod   time.Time
	blockingTypes map[string]bool
	filterInput   textinput.Model
	filtering     bool
	showHelp      bool
	help          components.Help
	ready         bool
	workAvail     bool
	projectDir    string
	inTmux        bool
	activeWorkers map[string]string // beadID -> tmux paneID

	// Toast notification
	toast components.Toast

	// Confetti animation
	confetti Confetti

	// Change indicators: track recently changed issue IDs
	changedIDs   map[string]bool
	changedAt    time.Time
	prevIssueMap map[string]data.Status // issueID -> previous status for diffing

	// Focus mode
	focusMode bool

	// Issue creation form
	creating   bool
	createForm components.CreateForm

	// Command palette
	showPalette bool
	palette     components.Palette
	startedAt   time.Time // guards ":" palette trigger during terminal negotiation

	// Data source mode (JSONL file watcher vs bd CLI polling)
	sourceMode data.SourceMode

	// Startup: issue ID from bd show --current, consumed after first parade build
	pendingCurrentID string
	currentIssueID   string // active issue shown in header

	// Layout preset (cycle with command palette)
	layoutPreset LayoutPreset

	// Bead string shimmer animation
	beadOffset int

	// Metadata schema from .beads/config.yaml
	metadataSchema *data.MetadataSchema

	// Workspace identity from bd context --json (fetched at startup)
	beadsContext *data.BeadsContext

	// Shared terminal control-sequence guard (used by both the Bubble Tea
	// filter and app-level deferred key handling).
	oscGuard *OSCGuard

	// Deferred printable key handling for non-text-entry modes.
	pendingKeys  []pendingDeferredKey
	pendingKeyID uint64
}

// New creates a new app model from loaded issues.
func New(issues []data.Issue, source data.Source, blockingTypes map[string]bool) Model {
	return NewWithGuard(issues, source, blockingTypes, nil)
}

// NewWithGuard creates a new app model from loaded issues and attaches a
// shared OSC guard when one is provided.
func NewWithGuard(issues []data.Issue, source data.Source, blockingTypes map[string]bool, guard *OSCGuard) Model {
	groups := data.GroupByParade(issues, blockingTypes)

	watchPath := source.Path
	pathExplicit := source.Explicit
	projectDir := source.ProjectDir

	lastFileMod := time.Time{}
	if watchPath != "" {
		if mod, err := data.FileModTime(watchPath); err == nil {
			lastFileMod = mod
		}
	}
	ti := textinput.New()
	ti.Prompt = ui.InputPrompt.Render("/ ")
	ti.Placeholder = "Filter type:bug, p1, or fuzzy text..."
	ti.SetWidth(50)

	// Build initial status snapshot for change detection
	prevMap := make(map[string]data.Status, len(issues))
	for _, iss := range issues {
		prevMap[iss.ID] = iss.Status
	}

	metaSchema := data.LoadMetadataSchema(projectDir)

	return Model{
		issues:         issues,
		groups:         groups,
		activPane:      PaneParade,
		watchPath:      watchPath,
		pathExplicit:   pathExplicit,
		lastFileMod:    lastFileMod,
		blockingTypes:  blockingTypes,
		filterInput:    ti,
		workAvail:      mg.WorkAvailable(),
		projectDir:     projectDir,
		inTmux:         mg.InTmux() && mg.TmuxAvailable(),
		activeWorkers:  make(map[string]string),
		changedIDs:     make(map[string]bool),
		prevIssueMap:   prevMap,
		sourceMode:     source.Mode,
		metadataSchema: metaSchema,
		startedAt:      time.Now(),
		oscGuard:       guard,
	}
}

// Init implements tea.Model.
func (m Model) Init() tea.Cmd {
	cmds := []tea.Cmd{
		m.startPoll(),
		headerShimmerCmd(),
	}
	if m.inTmux {
		cmds = append(cmds, pollWorkerState)
	}
	if m.sourceMode == data.SourceCLI {
		cmds = append(cmds, fetchCurrentIssue, fetchBeadsContext)
	}
	return tea.Batch(cmds...)
}

// fetchCurrentIssue asks bd for the active issue ID at startup.
func fetchCurrentIssue() tea.Msg {
	id, _ := data.FetchCurrentIssueID()
	return currentIssueMsg{issueID: id}
}

type beadsContextMsg struct {
	ctx *data.BeadsContext
}

// fetchBeadsContext runs bd context --json in the background at startup.
func fetchBeadsContext() tea.Msg {
	ctx, _ := data.FetchContext()
	return beadsContextMsg{ctx: ctx}
}

// startPoll returns the appropriate polling Cmd based on sourceMode.
func (m Model) startPoll() tea.Cmd {
	if m.sourceMode == data.SourceCLI {
		return data.PollCLI(m.projectDir)
	}
	return data.WatchFile(m.watchPath, m.lastFileMod)
}

// startPollImmediate returns an immediate-fetch Cmd for post-mutation refresh.
func (m Model) startPollImmediate() tea.Cmd {
	if m.sourceMode == data.SourceCLI {
		return data.FetchIssuesNow(m.projectDir)
	}
	return data.WatchFile(m.watchPath, m.lastFileMod)
}

// --- Worker message types ---

// workLaunchedMsg is sent when an oro work session starts in a tmux pane.
type workLaunchedMsg struct {
	beadID string
	paneID string
}

// workFinishedMsg is sent when an oro work process exits.
type workFinishedMsg struct{ err error }

// workErrorMsg is sent when launching a worker fails.
type workErrorMsg struct {
	beadID string
	err    error
}

// workerStatusMsg carries the current set of active worker panes.
type workerStatusMsg struct {
	active map[string]string
}

// --- Other message types ---

// mutateResultMsg is sent when a bd CLI mutation completes.
type mutateResultMsg struct {
	issueID string
	action  string
	err     error
}

// changeIndicatorExpiredMsg clears change indicators after timeout.
type changeIndicatorExpiredMsg struct{}

// currentIssueMsg carries the active issue ID from bd show --current at startup.
type currentIssueMsg struct {
	issueID string
}

// headerShimmerMsg drives the bead string shimmer animation.
type headerShimmerMsg struct{}

// issueDetailMsg carries enriched issue data from bd show.
type issueDetailMsg struct {
	issueID string
	issue   *data.Issue
	err     error
}

// Update implements tea.Model.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	logMsg(msg)

	skipDeferredKeyBuffer := false
	if deferred, ok := msg.(deferredKeyMsg); ok {
		var key tea.KeyPressMsg
		var resolved bool
		m, key, resolved = m.resolveDeferredKey(deferred)
		if !resolved {
			return m, nil
		}
		msg = key
		skipDeferredKeyBuffer = true
	}

	// Handle create form result
	if result, ok := msg.(components.CreateFormResult); ok {
		m.creating = false
		if result.Cancelled || result.Title == "" {
			return m, nil
		}
		title := result.Title
		issueType := data.IssueType(result.Type)
		priority := components.ParsePriority(result.Priority)
		return m, func() tea.Msg {
			_, err := data.CreateIssue(title, issueType, priority)
			return mutateResultMsg{issueID: title, action: "created", err: err}
		}
	}

	// Handle palette result
	if result, ok := msg.(components.PaletteResult); ok {
		m.showPalette = false
		if !result.Cancelled {
			return m.executePaletteAction(result.Action)
		}
		return m, nil
	}

	// Forward all messages to palette when active
	if m.showPalette {
		if km, ok := msg.(tea.KeyPressMsg); ok && km.String() == "ctrl+c" {
			logRoute("palette ctrl+c -> quit")
			return m, tea.Quit
		}
		logRoute("palette forward")
		var cmd tea.Cmd
		m.palette, cmd = m.palette.Update(msg)
		return m, cmd
	}

	// Forward all messages to create form when active
	if m.creating {
		if km, ok := msg.(tea.KeyPressMsg); ok && km.String() == "ctrl+c" {
			logRoute("createForm ctrl+c -> quit")
			return m, tea.Quit
		}
		logRoute("createForm forward")
		var cmd tea.Cmd
		m.createForm, cmd = m.createForm.Update(msg)
		return m, cmd
	}

	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		return m.handleKeyPress(msg, !skipDeferredKeyBuffer)

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.layout()
		m.ready = true
		return m, nil

	case data.FileChangedMsg:
		cmds := []tea.Cmd{m.startPoll()}
		if m.inTmux {
			cmds = append(cmds, pollWorkerState)
		}

		// Warn if malformed lines were skipped
		if msg.Skipped > 0 {
			toast, toastCmd := components.ShowToast(
				fmt.Sprintf("Skipped %d malformed line(s)", msg.Skipped),
				components.ToastWarn, toastDuration,
			)
			m.toast = toast
			cmds = append(cmds, toastCmd)
		}

		// Diff against previous state for change indicators
		changes := m.diffIssues(msg.Issues)
		if changes > 0 {
			m.changedAt = time.Now()
			toast, toastCmd := components.ShowToast(
				fmt.Sprintf("File reloaded \u2014 %d issue%s changed", changes, plural(changes)),
				components.ToastInfo, toastDuration,
			)
			m.toast = toast
			cmds = append(cmds, toastCmd)
			cmds = append(cmds, tea.Tick(changeIndicatorDuration, func(time.Time) tea.Msg {
				return changeIndicatorExpiredMsg{}
			}))
		}

		// Update snapshot for next diff
		m.prevIssueMap = make(map[string]data.Status, len(msg.Issues))
		for _, iss := range msg.Issues {
			m.prevIssueMap[iss.ID] = iss.Status
		}

		m.issues = msg.Issues
		m.groups = data.GroupByParade(msg.Issues, m.blockingTypes)
		if !msg.LastMod.IsZero() {
			m.lastFileMod = msg.LastMod
		}
		m.rebuildParade()
		return m, tea.Batch(cmds...)

	case data.FileUnchangedMsg:
		if !msg.LastMod.IsZero() {
			m.lastFileMod = msg.LastMod
		}
		cmds := []tea.Cmd{m.startPoll()}
		if m.inTmux {
			cmds = append(cmds, pollWorkerState)
		}
		return m, tea.Batch(cmds...)

	case data.FileWatchErrorMsg:
		cmds := []tea.Cmd{m.startPoll()}
		if m.inTmux {
			cmds = append(cmds, pollWorkerState)
		}
		label := fmt.Sprintf("Load failed: %s", msg.Err)
		if m.sourceMode == data.SourceCLI {
			label = fmt.Sprintf("bd list failed: %s", msg.Err)
		}
		toast, toastCmd := components.ShowToast(label, components.ToastError, toastDuration)
		m.toast = toast
		cmds = append(cmds, toastCmd)
		return m, tea.Batch(cmds...)

	case workLaunchedMsg:
		m.activeWorkers[msg.beadID] = msg.paneID
		m.propagateWorkerState()
		toast, cmd := components.ShowToast(
			fmt.Sprintf("Worker launched for %s", msg.beadID),
			components.ToastSuccess, toastDuration,
		)
		m.toast = toast
		return m, cmd

	case workErrorMsg:
		toast, cmd := components.ShowToast(
			fmt.Sprintf("Worker launch failed: %s", msg.err),
			components.ToastError, toastDuration,
		)
		m.toast = toast
		return m, cmd

	case workerStatusMsg:
		m.activeWorkers = msg.active
		m.propagateWorkerState()
		return m, nil

	case mutateResultMsg:
		if msg.err != nil {
			toast, cmd := components.ShowToast(
				fmt.Sprintf("Failed: %s %s \u2014 %s", msg.action, msg.issueID, msg.err),
				components.ToastError, toastDuration,
			)
			m.toast = toast
			return m, cmd
		}
		toast, toastCmd := components.ShowToast(
			fmt.Sprintf("%s \u2192 %s", msg.issueID, msg.action),
			components.ToastSuccess, toastDuration,
		)
		m.toast = toast
		// Force reload: reset lastFileMod for JSONL, or immediate fetch for CLI
		m.lastFileMod = time.Time{}
		cmds := []tea.Cmd{toastCmd, m.startPollImmediate()}

		// Trigger confetti on close
		if msg.action == "closed" && m.width > 0 && m.height > 0 {
			m.confetti = NewConfetti(m.width, m.height)
			cmds = append(cmds, m.confetti.Tick())
		}
		return m, tea.Batch(cmds...)

	case confettiTickMsg:
		m.confetti.Update()
		if m.confetti.Active() {
			return m, m.confetti.Tick()
		}
		return m, nil

	case headerShimmerMsg:
		m.beadOffset++
		return m, headerShimmerCmd()

	case components.ToastDismissMsg:
		m.toast = components.Toast{}
		return m, nil

	case changeIndicatorExpiredMsg:
		m.changedIDs = make(map[string]bool)
		m.parade.ChangedIDs = nil
		return m, nil

	case currentIssueMsg:
		if msg.issueID == "" {
			return m, nil
		}
		m.currentIssueID = msg.issueID
		if len(m.parade.Items) > 0 {
			m.restoreParadeSelection(msg.issueID)
			m.syncSelection()
		} else {
			m.pendingCurrentID = msg.issueID
		}
		return m, nil

	case beadsContextMsg:
		m.beadsContext = msg.ctx
		return m, nil

	case issueDetailMsg:
		if msg.err == nil && msg.issue != nil {
			if m.detail.Issue != nil && m.detail.Issue.ID == msg.issueID {
				m.detail.SetRichDetail(msg.issueID, msg.issue)
			}
		}
		return m, nil

	case workFinishedMsg:
		// Reset lastFileMod to force reload on next poll cycle.
		m.lastFileMod = time.Time{}
		cmds := []tea.Cmd{m.startPollImmediate()}
		if m.inTmux {
			cmds = append(cmds, pollWorkerState)
		}
		return m, tea.Batch(cmds...)
	}

	// Forward to detail viewport when focused
	if m.activPane == PaneDetail {
		var cmd tea.Cmd
		m.detail.Viewport, cmd = m.detail.Viewport.Update(msg)
		return m, cmd
	}

	return m, nil
}

func (m Model) handleHelpKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "q", "?":
		m.showHelp = false
		return m, nil
	default:
		// Route page navigation to help component
		m.help, _ = m.help.Update(msg)
		return m, nil
	}
}

func (m Model) handleFilteringKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q":
		return m, tea.Quit
	case "?":
		m.showHelp = true
		return m, nil
	case "esc":
		m.filtering = false
		m.filterInput.SetValue("")
		m.filterInput.Blur()
		m.rebuildParade()
		return m, nil
	case "enter":
		m.filtering = false
		m.filterInput.Blur()
		return m, nil
	}

	var cmd tea.Cmd
	oldVal := m.filterInput.Value()
	m.filterInput, cmd = m.filterInput.Update(msg)
	if m.filterInput.Value() != oldVal {
		m.rebuildParade()
	}
	return m, cmd
}

func (m Model) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	str := msg.String()
	ks := msg.Keystroke()
	if str != ks {
		dbg("  handleKey: String=%q Keystroke=%q (DIFFER)", str, ks)
	}

	switch str {
	case "q":
		logAction("quit")
		return m, tea.Quit

	case "?":
		logAction("help")
		m.showHelp = true
		return m, nil

	case "/":
		m.filtering = true
		m.filterInput.Focus()
		return m, textinput.Blink

	case "tab":
		if m.activPane == PaneParade {
			m.activPane = PaneDetail
			m.detail.Focused = true
		} else {
			m.activPane = PaneParade
			m.detail.Focused = false
		}
		return m, nil

	case "esc":
		if m.focusMode {
			m.focusMode = false
			m.rebuildParade()
			return m, nil
		}
		if m.activPane == PaneDetail {
			m.activPane = PaneParade
			m.detail.Focused = false
		}
		return m, nil

	case "f":
		m.focusMode = !m.focusMode
		m.rebuildParade()
		if m.focusMode {
			toast, cmd := components.ShowToast("Focus mode ON", components.ToastInfo, toastDuration)
			m.toast = toast
			return m, cmd
		}
		toast, cmd := components.ShowToast("Focus mode OFF", components.ToastInfo, toastDuration)
		m.toast = toast
		return m, cmd

	case "c":
		m.parade.ToggleClosed()
		m.syncSelection()
		return m, nil

	// Quick actions: status changes
	case "1":
		return m.quickAction(data.StatusInProgress, "in_progress")
	case "2":
		return m.quickAction(data.StatusOpen, "open")
	case "3":
		return m.closeSelectedIssue()

	// Quick actions: priority changes
	case "!": // Shift+1
		return m.setPriority(data.PriorityHigh)
	case "@": // Shift+2
		return m.setPriority(data.PriorityMedium)
	case "#": // Shift+3
		return m.setPriority(data.PriorityLow)
	case "$": // Shift+4
		return m.setPriority(data.PriorityBacklog)

	// Git branch name copy
	case "b":
		return m.copyBranchName()
	case "B":
		return m.createAndSwitchBranch()

	// Launch oro work on selected bead
	case "w":
		issue := m.parade.SelectedIssue
		if issue == nil || !m.workAvail {
			return m, nil
		}
		// If already active, switch focus to that pane
		if _, active := m.activeWorkers[issue.ID]; active && m.inTmux {
			_ = mg.SelectWorkerPane(issue.ID)
			return m, nil
		}
		beadID := issue.ID
		if m.inTmux {
			return m, func() tea.Msg {
				paneID, err := mg.LaunchWorkInTmux(beadID, m.projectDir)
				if err != nil {
					return workErrorMsg{beadID: beadID, err: err}
				}
				return workLaunchedMsg{beadID: beadID, paneID: paneID}
			}
		}
		c := mg.WorkCommand(beadID, m.projectDir)
		return m, tea.ExecProcess(c, func(err error) tea.Msg {
			return workFinishedMsg{err: err}
		})

	// Kill active worker pane
	case "W":
		issue := m.parade.SelectedIssue
		if issue == nil {
			return m, nil
		}
		if _, active := m.activeWorkers[issue.ID]; !active {
			return m, nil
		}
		beadID := issue.ID
		if m.inTmux {
			return m, func() tea.Msg {
				_ = mg.KillWorkerPane(beadID)
				panes, _ := mg.PollWorkerPanes()
				if panes == nil {
					panes = make(map[string]string)
				}
				return workerStatusMsg{active: panes}
			}
		}
		return m, nil

	case "N":
		m.creating = true
		m.createForm = components.NewCreateForm(m.width, m.height)
		return m, m.createForm.Init()

	case "ctrl+k":
		m.showPalette = true
		m.palette = components.NewPalette(m.width, m.height, m.buildPaletteCommands())
		return m, m.palette.Init()
	case ":":
		m.showPalette = true
		m.palette = components.NewPalette(m.width, m.height, m.buildPaletteCommands())
		return m, m.palette.Init()
	}

	// Navigation keys depend on active pane
	if m.activPane == PaneParade {
		logAction("parade nav: %s", str)
		switch str {
		case "j", "down":
			m.parade.MoveDown()
			m.syncSelection()
		case "k", "up":
			m.parade.MoveUp()
			m.syncSelection()
		case "J": // Shift+J: select + move down
			m.parade.ToggleSelect()
			m.parade.MoveDown()
			m.syncSelection()
		case "K": // Shift+K: select + move up
			m.parade.ToggleSelect()
			m.parade.MoveUp()
			m.syncSelection()
		case "space", "x": // Toggle multi-select
			m.parade.ToggleSelect()
		case "X": // Clear all selections
			m.parade.ClearSelection()
		case "g":
			m.parade.Cursor = 0
			m.parade.ScrollOffset = 0
			for i, item := range m.parade.Items {
				if !item.IsHeader {
					m.parade.Cursor = i
					break
				}
			}
			m.syncSelection()
		case "G":
			for i := len(m.parade.Items) - 1; i >= 0; i-- {
				if !m.parade.Items[i].IsHeader {
					m.parade.Cursor = i
					break
				}
			}
			m.syncSelection()
		case "enter":
			m.activPane = PaneDetail
			m.detail.Focused = true
			if cmd := m.maybeFetchIssueDetail(); cmd != nil {
				return m, cmd
			}
		}
		return m, nil
	}

	// Detail pane navigation
	if m.activPane == PaneDetail {
		logAction("detail nav: %s", str)
		var cmd tea.Cmd
		switch str {
		case "j", "down":
			m.detail.Viewport.ScrollDown(1)
		case "k", "up":
			m.detail.Viewport.ScrollUp(1)
		default:
			m.detail.Viewport, cmd = m.detail.Viewport.Update(msg)
		}
		return m, cmd
	}

	dbg("  UNHANDLED key: String=%q Keystroke=%q pane=%d", str, ks, m.activPane)
	return m, nil
}

// quickAction runs bd update to change issue status. Works on multi-selection if active.
func (m Model) quickAction(status data.Status, label string) (tea.Model, tea.Cmd) {
	// Bulk mode: apply to all selected issues
	if selected := m.parade.SelectedIssues(); len(selected) > 0 {
		issues := selected
		count := len(issues)
		m.parade.ClearSelection()
		return m, func() tea.Msg {
			var lastErr error
			for _, iss := range issues {
				if iss.Status != status {
					if err := data.SetStatus(iss.ID, status); err != nil {
						lastErr = err
					}
				}
			}
			return mutateResultMsg{
				issueID: fmt.Sprintf("%d issues", count),
				action:  label,
				err:     lastErr,
			}
		}
	}

	issue := m.parade.SelectedIssue
	if issue == nil {
		return m, nil
	}
	if issue.Status == status {
		return m, nil
	}
	issueID := issue.ID
	return m, func() tea.Msg {
		var err error
		if status == data.StatusInProgress {
			err = data.ClaimIssue(issueID)
		} else {
			err = data.SetStatus(issueID, status)
		}
		return mutateResultMsg{issueID: issueID, action: label, err: err}
	}
}

// closeSelectedIssue runs bd close on the selected issue(s).
func (m Model) closeSelectedIssue() (tea.Model, tea.Cmd) {
	// Bulk mode
	if selected := m.parade.SelectedIssues(); len(selected) > 0 {
		issues := selected
		count := len(issues)
		m.parade.ClearSelection()
		return m, func() tea.Msg {
			var lastErr error
			for _, iss := range issues {
				if iss.Status != data.StatusClosed {
					if err := data.CloseIssue(iss.ID); err != nil {
						lastErr = err
					}
				}
			}
			return mutateResultMsg{
				issueID: fmt.Sprintf("%d issues", count),
				action:  "closed",
				err:     lastErr,
			}
		}
	}

	issue := m.parade.SelectedIssue
	if issue == nil {
		return m, nil
	}
	if issue.Status == data.StatusClosed {
		return m, nil
	}
	issueID := issue.ID
	return m, func() tea.Msg {
		err := data.CloseIssue(issueID)
		return mutateResultMsg{issueID: issueID, action: "closed", err: err}
	}
}

// setPriority runs bd update to change issue priority. Works on multi-selection if active.
func (m Model) setPriority(priority data.Priority) (tea.Model, tea.Cmd) {
	// Bulk mode
	if selected := m.parade.SelectedIssues(); len(selected) > 0 {
		issues := selected
		count := len(issues)
		label := fmt.Sprintf("P%d", priority)
		m.parade.ClearSelection()
		return m, func() tea.Msg {
			var lastErr error
			for _, iss := range issues {
				if iss.Priority != priority {
					if err := data.SetPriority(iss.ID, priority); err != nil {
						lastErr = err
					}
				}
			}
			return mutateResultMsg{
				issueID: fmt.Sprintf("%d issues", count),
				action:  label,
				err:     lastErr,
			}
		}
	}

	issue := m.parade.SelectedIssue
	if issue == nil {
		return m, nil
	}
	if issue.Priority == priority {
		return m, nil
	}
	issueID := issue.ID
	label := fmt.Sprintf("P%d", priority)
	return m, func() tea.Msg {
		err := data.SetPriority(issueID, priority)
		return mutateResultMsg{issueID: issueID, action: label, err: err}
	}
}

// copyBranchName copies a slugified branch name to the clipboard.
func (m Model) copyBranchName() (tea.Model, tea.Cmd) {
	issue := m.parade.SelectedIssue
	if issue == nil {
		return m, nil
	}
	branch := data.BranchName(*issue)
	err := clipboard.WriteAll(branch)
	if err != nil {
		toast, cmd := components.ShowToast(
			fmt.Sprintf("Clipboard error: %s", err),
			components.ToastError, toastDuration,
		)
		m.toast = toast
		return m, cmd
	}
	toast, cmd := components.ShowToast(
		fmt.Sprintf("Copied: %s", branch),
		components.ToastSuccess, toastDuration,
	)
	m.toast = toast
	return m, cmd
}

// createAndSwitchBranch creates a git branch and switches to it.
func (m Model) createAndSwitchBranch() (tea.Model, tea.Cmd) {
	issue := m.parade.SelectedIssue
	if issue == nil {
		return m, nil
	}
	branch := data.BranchName(*issue)
	issueCopy := *issue
	return m, func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		err := createBranchCmd(ctx, m.projectDir, branch).Run()
		action := fmt.Sprintf("branch: %s", branch)
		if err != nil {
			return mutateResultMsg{issueID: issueCopy.ID, action: action, err: err}
		}
		return mutateResultMsg{issueID: issueCopy.ID, action: action}
	}
}

func createBranchCmd(ctx context.Context, projectDir, branch string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "git", "checkout", "-b", branch)
	if projectDir != "" {
		cmd.Dir = projectDir
	}
	return cmd
}

// buildPaletteCommands returns the context-aware list of palette commands.
func (m Model) buildPaletteCommands() []components.PaletteCommand {
	cmds := []components.PaletteCommand{
		{Name: "Set status: in_progress", Desc: "Mark issue as rolling", Key: "1", Action: components.ActionSetInProgress},
		{Name: "Set status: open", Desc: "Mark issue as lined up", Key: "2", Action: components.ActionSetOpen},
		{Name: "Close issue", Desc: "Mark issue as closed", Key: "3", Action: components.ActionCloseIssue},
		{Name: "Set priority: P1 high", Desc: "Urgent work", Key: "!", Action: components.ActionSetPriorityHigh},
		{Name: "Set priority: P2 medium", Desc: "Normal priority", Key: "@", Action: components.ActionSetPriorityMedium},
		{Name: "Set priority: P3 low", Desc: "Can wait", Key: "#", Action: components.ActionSetPriorityLow},
		{Name: "Set priority: P4 backlog", Desc: "Someday maybe", Key: "$", Action: components.ActionSetPriorityBacklog},
		{Name: "Copy branch name", Desc: "Copy git branch to clipboard", Key: "b", Action: components.ActionCopyBranch},
		{Name: "Create git branch", Desc: "Checkout new branch for issue", Key: "B", Action: components.ActionCreateBranch},
		{Name: "New issue", Desc: "Create a new beads issue", Key: "N", Action: components.ActionNewIssue},
		{Name: "Toggle focus mode", Desc: "Show only my work + top priority", Key: "f", Action: components.ActionToggleFocus},
		{Name: "Toggle closed issues", Desc: "Show/hide past the stand", Key: "c", Action: components.ActionToggleClosed},
		{Name: "Filter", Desc: "Fuzzy filter the parade list", Key: "/", Action: components.ActionFilter},
		{Name: "Help", Desc: "Show keybinding help", Key: "?", Action: components.ActionHelp},
		{Name: "Quit", Desc: "Exit Mardi Gras", Key: "q", Action: components.ActionQuit},
		{Name: "Cycle layout", Desc: "Switch panel arrangement", Key: "", Action: components.ActionCycleLayout},
	}

	if m.workAvail {
		cmds = append(cmds,
			components.PaletteCommand{Name: "Launch worker", Desc: "Start oro work on issue", Key: "w", Action: components.ActionLaunchWork},
		)
	}

	return cmds
}

// executePaletteAction maps a palette action to an existing method.
func (m Model) executePaletteAction(action components.PaletteAction) (tea.Model, tea.Cmd) {
	switch action {
	case components.ActionSetInProgress:
		return m.quickAction(data.StatusInProgress, "in_progress")
	case components.ActionSetOpen:
		return m.quickAction(data.StatusOpen, "open")
	case components.ActionCloseIssue:
		return m.closeSelectedIssue()
	case components.ActionSetPriorityHigh:
		return m.setPriority(data.PriorityHigh)
	case components.ActionSetPriorityMedium:
		return m.setPriority(data.PriorityMedium)
	case components.ActionSetPriorityLow:
		return m.setPriority(data.PriorityLow)
	case components.ActionSetPriorityBacklog:
		return m.setPriority(data.PriorityBacklog)
	case components.ActionCopyBranch:
		return m.copyBranchName()
	case components.ActionCreateBranch:
		return m.createAndSwitchBranch()
	case components.ActionNewIssue:
		m.creating = true
		m.createForm = components.NewCreateForm(m.width, m.height)
		return m, m.createForm.Init()
	case components.ActionToggleFocus:
		m.focusMode = !m.focusMode
		m.rebuildParade()
		label := "Focus mode ON"
		if !m.focusMode {
			label = "Focus mode OFF"
		}
		toast, cmd := components.ShowToast(label, components.ToastInfo, toastDuration)
		m.toast = toast
		return m, cmd
	case components.ActionToggleClosed:
		m.parade.ToggleClosed()
		m.syncSelection()
		return m, nil
	case components.ActionFilter:
		m.filtering = true
		m.filterInput.Focus()
		return m, textinput.Blink
	case components.ActionLaunchWork:
		return m.handleKey(tea.KeyPressMsg{Code: 'w', Text: "w"})
	case components.ActionCycleLayout:
		m.layoutPreset = (m.layoutPreset + 1) % layoutPresetCount
		labels := [...]string{"Default", "Wide"}
		toast, cmd := components.ShowToast("Layout: "+labels[m.layoutPreset], components.ToastInfo, toastDuration)
		m.toast = toast
		m.layout()
		return m, cmd
	case components.ActionHelp:
		m.showHelp = true
		return m, nil
	case components.ActionQuit:
		return m, tea.Quit
	}
	return m, nil
}

// maybeFetchIssueDetail returns a Cmd to fetch rich detail for the selected issue.
func (m *Model) maybeFetchIssueDetail() tea.Cmd {
	issue := m.parade.SelectedIssue
	if issue == nil {
		return nil
	}
	if m.detail.RichIssueID == issue.ID {
		return nil
	}
	return fetchIssueDetail(issue.ID)
}

// fetchIssueDetail returns a Cmd that fetches rich detail for an issue.
func fetchIssueDetail(issueID string) tea.Cmd {
	return func() tea.Msg {
		issue, err := data.FetchIssueDetail(issueID)
		return issueDetailMsg{issueID: issueID, issue: issue, err: err}
	}
}

// diffIssues compares new issues against the previous snapshot and returns the count of changes.
func (m *Model) diffIssues(newIssues []data.Issue) int {
	if len(m.prevIssueMap) == 0 {
		return 0
	}

	changed := 0
	newMap := make(map[string]data.Status, len(newIssues))
	for _, iss := range newIssues {
		newMap[iss.ID] = iss.Status
	}

	// Check for status changes or new issues
	for id, newStatus := range newMap {
		oldStatus, existed := m.prevIssueMap[id]
		if !existed || oldStatus != newStatus {
			m.changedIDs[id] = true
			changed++
		}
	}

	// Check for removed issues
	for id := range m.prevIssueMap {
		if _, exists := newMap[id]; !exists {
			changed++
		}
	}

	return changed
}

// syncSelection updates the detail panel with the currently selected issue.
func (m *Model) syncSelection() {
	if m.parade.SelectedIssue != nil {
		m.detail.SetIssue(m.parade.SelectedIssue)
		return
	}
	m.detail.SetIssue(nil)
}

// layout recalculates dimensions for all sub-components.
func (m *Model) layout() {
	headerH := 2
	footerH := 2
	bodyH := m.height - headerH - footerH
	if bodyH < 1 {
		bodyH = 1
	}

	var paradeW, detailW int
	switch m.layoutPreset {
	case LayoutWide:
		paradeW = m.width
		detailW = 0
	default:
		paradeW = m.width * 2 / 5
		if paradeW < 30 {
			paradeW = 30
		}
		detailW = m.width - paradeW
	}

	m.header = components.Header{
		Width:          m.width,
		Groups:         m.groups,
		WorkerCount:    len(m.activeWorkers),
		BeadOffset:     m.beadOffset,
		CurrentIssueID: m.currentIssueID,
	}

	m.parade.SetSize(paradeW, bodyH)
	m.detail.SetSize(detailW, bodyH)
	m.detail.AllIssues = m.issues
	detailIssueMap := data.BuildIssueMap(m.issues)
	m.detail.IssueMap = detailIssueMap
	m.detail.BlockingTypes = m.blockingTypes
	m.detail.MetadataSchema = m.metadataSchema

	if len(m.parade.Items) == 0 {
		m.parade = views.NewParadeWithData(m.issues, m.groups, detailIssueMap, paradeW, bodyH, m.blockingTypes)
		m.syncSelection()
		if m.pendingCurrentID != "" {
			m.restoreParadeSelection(m.pendingCurrentID)
			m.syncSelection()
			m.pendingCurrentID = ""
		}
	}

	m.detail.Viewport = viewport.New(viewport.WithWidth(detailW-2), viewport.WithHeight(bodyH))
	m.propagateWorkerState()
	if m.parade.SelectedIssue != nil {
		m.detail.SetIssue(m.parade.SelectedIssue)
	}
}

// rebuildParade reconstructs the parade from current issues, preserving selection if possible.
func (m *Model) rebuildParade() {
	oldSelectedID := ""
	if m.parade.SelectedIssue != nil {
		oldSelectedID = m.parade.SelectedIssue.ID
	}
	oldShowClosed := m.parade.ShowClosed

	paradeW := m.parade.Width
	bodyH := m.parade.Height
	if paradeW == 0 {
		paradeW = m.width * 2 / 5
	}
	if bodyH == 0 {
		bodyH = m.height - 4
	}

	filteredIssues, highlights := data.FilterIssuesWithHighlights(m.issues, m.filterInput.Value())
	if m.focusMode {
		filteredIssues = data.FocusFilter(filteredIssues, m.blockingTypes)
	}
	groups := m.groups
	detailIssueMap := data.BuildIssueMap(m.issues)
	paradeIssueMap := detailIssueMap
	if m.filterInput.Value() != "" || m.focusMode {
		groups = data.GroupByParade(filteredIssues, m.blockingTypes)
		paradeIssueMap = data.BuildIssueMap(filteredIssues)
	}

	m.header = components.Header{
		Width:          m.width,
		Groups:         groups,
		WorkerCount:    len(m.activeWorkers),
		BeadOffset:     m.beadOffset,
		CurrentIssueID: m.currentIssueID,
	}

	m.parade = views.NewParadeWithData(filteredIssues, groups, paradeIssueMap, paradeW, bodyH, m.blockingTypes)
	m.parade.MatchHighlights = highlights
	if oldShowClosed {
		m.parade.ToggleClosed()
	}
	m.restoreParadeSelection(oldSelectedID)

	// Propagate change indicators to parade
	m.parade.ChangedIDs = m.changedIDs

	m.detail.AllIssues = m.issues
	m.detail.IssueMap = detailIssueMap
	m.detail.BlockingTypes = m.blockingTypes
	m.propagateWorkerState()
	m.syncSelection()
}

// restoreParadeSelection restores selection by issue ID when possible.
func (m *Model) restoreParadeSelection(issueID string) {
	if issueID == "" {
		return
	}
	for i, item := range m.parade.Items {
		if item.IsHeader || item.Issue == nil || item.Issue.ID != issueID {
			continue
		}
		m.parade.Cursor = i
		m.parade.SelectedIssue = item.Issue

		if m.parade.Cursor < m.parade.ScrollOffset {
			m.parade.ScrollOffset = m.parade.Cursor
		}
		if m.parade.Cursor >= m.parade.ScrollOffset+m.parade.Height {
			m.parade.ScrollOffset = m.parade.Cursor - m.parade.Height + 1
		}

		maxOffset := len(m.parade.Items) - m.parade.Height
		if maxOffset < 0 {
			maxOffset = 0
		}
		if m.parade.ScrollOffset > maxOffset {
			m.parade.ScrollOffset = maxOffset
		}
		if m.parade.ScrollOffset < 0 {
			m.parade.ScrollOffset = 0
		}
		return
	}
}

// propagateWorkerState pushes active worker info to all sub-views.
func (m *Model) propagateWorkerState() {
	m.parade.ActiveWorkers = m.activeWorkers
	m.detail.ActiveWorkers = m.activeWorkers
	m.header.WorkerCount = len(m.activeWorkers)

	if m.detail.Issue != nil {
		m.detail.SetIssue(m.detail.Issue)
	}
}

// pollWorkerState queries tmux for panes tagged with @oro_mg_work.
func pollWorkerState() tea.Msg {
	panes, err := mg.PollWorkerPanes()
	if err != nil {
		return workerStatusMsg{active: make(map[string]string)}
	}
	return workerStatusMsg{active: panes}
}

const headerShimmerInterval = 500 * time.Millisecond

// headerShimmerCmd returns a Cmd that fires a headerShimmerMsg for bead animation.
func headerShimmerCmd() tea.Cmd {
	return tea.Tick(headerShimmerInterval, func(time.Time) tea.Msg {
		return headerShimmerMsg{}
	})
}

// altView wraps a string as a tea.View with AltScreen enabled.
func altView(s string) tea.View {
	v := tea.NewView(s)
	v.AltScreen = true
	return v
}

// View implements tea.Model.
func (m Model) View() tea.View {
	if !m.ready {
		return altView("Loading...")
	}

	header := m.header.View()

	var body string
	if m.layoutPreset == LayoutWide {
		body = m.parade.View()
	} else {
		body = lipgloss.JoinHorizontal(
			lipgloss.Top,
			m.parade.View(),
			m.detail.View(),
		)
	}

	inputBarStyle := lipgloss.NewStyle().Padding(0, 1).Width(m.width)
	var bottomBar string
	switch {
	case m.toast.Active():
		bottomBar = m.toast.View(m.width)
	case m.parade.SelectionCount() > 0:
		bottomBar = components.BulkFooter(m.width, m.parade.SelectionCount())
	case m.filtering || m.filterInput.Value() != "":
		bottomBar = inputBarStyle.Render(m.filterInput.View())
	default:
		footer := components.NewFooter(m.width, m.activPane == PaneDetail)
		footer.SourcePath = m.watchPath
		footer.LastRefresh = m.lastFileMod
		footer.PathExplicit = m.pathExplicit
		footer.SourceMode = m.sourceMode
		footer.BeadsContext = m.beadsContext
		bottomBar = footer.View()
	}

	divider := components.Divider(m.width)

	screen := lipgloss.JoinVertical(
		lipgloss.Left,
		header,
		body,
		divider,
		bottomBar,
	)

	// Confetti overlay
	if m.confetti.Active() {
		overlay := m.confetti.View()
		if overlay != "" {
			screen = overlayStrings(screen, overlay)
		}
	}

	if m.showPalette {
		return altView(m.palette.View())
	}

	if m.showHelp {
		m.help.SetSize(m.width, m.height)
		helpModal := m.help.View()
		return altView(lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, helpModal))
	}

	if m.creating {
		formTitle := ui.HelpTitle.Render("[ NEW ISSUE ]")
		formBody := m.createForm.View()
		formHint := ui.HelpHint.Render("esc to cancel")
		formContent := lipgloss.JoinVertical(lipgloss.Left, formTitle, "", formBody, "", formHint)
		formBox := ui.HelpOverlayBg.Width(m.width - 8).Render(formContent)
		return altView(lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, formBox))
	}

	return altView(screen)
}

// overlayStrings composites non-space characters from overlay onto base.
func overlayStrings(base, overlay string) string {
	baseLines := splitLines(base)
	overlayLines := splitLines(overlay)

	for y := 0; y < len(overlayLines) && y < len(baseLines); y++ {
		baseRunes := []rune(baseLines[y])
		overlayRunes := []rune(overlayLines[y])
		for x := 0; x < len(overlayRunes) && x < len(baseRunes); x++ {
			if overlayRunes[x] != ' ' {
				baseRunes[x] = overlayRunes[x]
			}
		}
		baseLines[y] = string(baseRunes)
	}

	return joinLines(baseLines)
}

func splitLines(s string) []string {
	var lines []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			lines = append(lines, s[start:i])
			start = i + 1
		}
	}
	lines = append(lines, s[start:])
	return lines
}

func joinLines(lines []string) string {
	result := ""
	for i, line := range lines {
		if i > 0 {
			result += "\n"
		}
		result += line
	}
	return result
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
