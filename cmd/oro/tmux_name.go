package main

// TmuxSessionName returns the tmux session name for a project.
// Empty project returns "oro" for backward compatibility.
// Non-empty project returns "oro-<project>" for multi-project isolation.
func TmuxSessionName(project string) string {
	if project == "" {
		return "oro"
	}
	return "oro-" + project
}

// TmuxPaneTarget returns a tmux pane target string (<session>:<role>)
// for the given project and role (e.g. "architect", "manager").
func TmuxPaneTarget(project, role string) string {
	return TmuxSessionName(project) + ":" + role
}
