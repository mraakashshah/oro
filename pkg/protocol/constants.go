package protocol

// Directory and path constants used throughout Oro.
const (
	// WorktreesDir is the directory where git worktrees are created.
	WorktreesDir = ".worktrees"

	// OroDir is the user-level state directory (e.g., ~/.oro).
	OroDir = ".oro"

	// BeadsDir is the historical internal name for Oro's task data directory.
	// New installs use .oro/tasks; the exported name remains for compatibility
	// with internal APIs that have not been renamed yet.
	BeadsDir = ".oro/tasks"

	// BranchPrefix is the git branch prefix for agent worktrees.
	BranchPrefix = "agent/"

	// EpicBranchPrefix is the git branch prefix for epic worktrees.
	EpicBranchPrefix = "epic/"
)
