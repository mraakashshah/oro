package data

import (
	"context"
	"fmt"
	"strings"

	"oro/pkg/beadstore"
)

// SetStatus changes an issue's status through the bead store.
//
//oro:testonly
func SetStatus(store beadstore.Store, issueID string, status Status) error {
	if store == nil {
		return fmt.Errorf("bead store is nil")
	}
	nextStatus := string(status)
	return store.Update(context.Background(), issueID, beadstore.UpdateParams{Status: &nextStatus})
}

// ClaimIssue marks an issue in progress and records the current user as owner.
//
//oro:testonly
func ClaimIssue(store beadstore.Store, issueID string) error {
	if store == nil {
		return fmt.Errorf("bead store is nil")
	}
	ctx := context.Background()
	bead, err := store.Show(ctx, issueID)
	if err != nil {
		return err
	}
	if bead == nil {
		return fmt.Errorf("bead %s not found", issueID)
	}
	owner := currentUser()
	if owner != "" && bead.Owner != "" && !strings.EqualFold(bead.Owner, owner) {
		return fmt.Errorf("bead %s is already claimed by %s", issueID, bead.Owner)
	}
	status := string(StatusInProgress)
	return store.Update(ctx, issueID, beadstore.UpdateParams{
		Status: &status,
		Owner:  &owner,
	})
}

// CloseIssue closes an issue through the bead store.
//
//oro:testonly
func CloseIssue(store beadstore.Store, issueID string) error {
	if store == nil {
		return fmt.Errorf("bead store is nil")
	}
	return store.Close(context.Background(), issueID, "")
}

// SetPriority changes an issue priority through the bead store.
//
//oro:testonly
func SetPriority(store beadstore.Store, issueID string, priority Priority) error {
	if store == nil {
		return fmt.Errorf("bead store is nil")
	}
	nextPriority := int(priority)
	return store.Update(context.Background(), issueID, beadstore.UpdateParams{Priority: &nextPriority})
}

// CreateIssue creates an issue through the bead store and returns the new issue ID.
//
//oro:testonly
func CreateIssue(store beadstore.Store, title string, issueType IssueType, priority Priority) (string, error) {
	if store == nil {
		return "", fmt.Errorf("bead store is nil")
	}
	bead, err := store.Create(context.Background(), beadstore.CreateParams{
		Title:    title,
		Type:     string(issueType),
		Priority: int(priority),
	})
	if err != nil {
		return "", err
	}
	if bead == nil {
		return "", fmt.Errorf("bead store returned nil created bead")
	}
	return strings.TrimSpace(bead.ID), nil
}

// BranchName generates a git branch name from an issue.
//
//oro:testonly
func BranchName(issue Issue) string {
	prefix := "feat"
	switch issue.IssueType {
	case TypeBug:
		prefix = "fix"
	case TypeChore:
		prefix = "chore"
	case TypeTask:
		prefix = "task"
	}
	slug := slugify(issue.Title)
	return fmt.Sprintf("%s/%s-%s", prefix, issue.ID, slug)
}

// slugify converts a title to a URL-safe slug.
func slugify(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	lastDash := false
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastDash = false
		case r == ' ', r == '-', r == '_', r == '/':
			if !lastDash && b.Len() > 0 {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}
	result := b.String()
	result = strings.TrimRight(result, "-")
	if len(result) > 50 {
		result = result[:50]
		result = strings.TrimRight(result, "-")
	}
	return result
}
