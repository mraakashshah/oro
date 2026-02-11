package protocol

// Directory and path constants used throughout Oro.
const (
	// WorktreesDir is the directory where git worktrees are created.
	WorktreesDir = ".worktrees"

	// OroDir is the user-level state directory (e.g., ~/.oro).
	OroDir = ".oro"

	// BeadsDir is the directory watched for bead file changes.
	BeadsDir = ".beads"

	// BranchPrefix is the git branch prefix for agent worktrees.
	BranchPrefix = "agent/"
)
