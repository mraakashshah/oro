package processenv

import "strings"

var gitEnvKeys = map[string]struct{}{
	"GIT_COMMON_DIR": {},
	"GIT_DIR":        {},
	"GIT_INDEX_FILE": {},
	"GIT_PREFIX":     {},
	"GIT_WORK_TREE":  {},
}

// ForWorkdir returns env with git worktree override variables stripped and PWD
// aligned with workdir. cmd.Dir changes the process cwd, but many nested tools
// inspect env PWD or git override variables before consulting the OS cwd.
func ForWorkdir(env []string, workdir string) []string {
	out := make([]string, 0, len(env)+1)
	pwdSet := workdir == ""
	for _, e := range env {
		key, _, ok := strings.Cut(e, "=")
		if ok {
			if _, strip := gitEnvKeys[key]; strip {
				continue
			}
			if key == "PWD" {
				if workdir != "" {
					out = append(out, "PWD="+workdir)
				}
				pwdSet = true
				continue
			}
		}
		out = append(out, e)
	}
	if !pwdSet {
		out = append(out, "PWD="+workdir)
	}
	return out
}
