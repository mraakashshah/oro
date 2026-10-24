package factory

import (
	"context"
	"fmt"
	"path/filepath"
)

// validateRepository establishes the containment boundary before policy reads or
// state creation. A directory nested beneath a repository is not that boundary.
func validateRepository(ctx context.Context, spec Spec) error {
	if err := validateSpec(spec); err != nil {
		return err
	}
	repo := gitRepository{path: spec.Repository}
	root, err := repo.run(ctx, spec.Repository, "rev-parse", "--show-toplevel")
	if err != nil {
		return fmt.Errorf("resolve Git working-tree root: %w", err)
	}
	actual, err := filepath.EvalSymlinks(root)
	if err != nil {
		return fmt.Errorf("resolve working-tree root path: %w", err)
	}
	requested, err := filepath.EvalSymlinks(spec.Repository)
	if err != nil {
		return fmt.Errorf("resolve requested repository path: %w", err)
	}
	if actual != requested {
		return fmt.Errorf("repository must be a Git working-tree root: requested %q, actual root %q", spec.Repository, root)
	}
	return nil
}
