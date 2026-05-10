package git

import (
	"bytes"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

// IsWorktree reports whether path is a linked git worktree (not the main one).
// It runs `git -C <path> rev-parse --git-dir` and checks whether the result
// points inside a `.git/worktrees/` directory.
func IsWorktree(path string) (bool, error) {
	out, err := run(path, "rev-parse", "--git-dir")
	if err != nil {
		return false, err
	}
	// Main worktree returns ".git" or an absolute path ending in ".git".
	// Linked worktrees return something like /abs/path/.git/worktrees/<name>.
	return strings.Contains(out, ".git/worktrees/"), nil
}

// FindMainWorktreeRoot returns the root directory of the main worktree for
// any path inside a git repository (main or linked).
func FindMainWorktreeRoot(path string) (string, error) {
	// --git-common-dir returns the shared .git dir (same for all worktrees).
	out, err := run(path, "rev-parse", "--git-common-dir")
	if err != nil {
		return "", err
	}
	// out is something like /abs/path/.git  or  .git (relative, for main worktree)
	gitDir := out
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(path, gitDir)
	}
	gitDir = filepath.Clean(gitDir)
	// Strip trailing /.git to get the repo root
	root := strings.TrimSuffix(gitDir, "/.git")
	root = strings.TrimSuffix(root, string(filepath.Separator)+".git")
	return root, nil
}

// RemoveWorktree removes a linked worktree. path may be any directory inside
// the worktree (e.g. a subdirectory the agent was working in); it is resolved
// to the worktree root via --show-toplevel before removal.
func RemoveWorktree(path string) error {
	// Resolve to the actual worktree root in case path is a subdirectory.
	worktreeRoot, err := run(path, "rev-parse", "--show-toplevel")
	if err != nil {
		return fmt.Errorf("resolving worktree root: %w", err)
	}
	mainRoot, err := FindMainWorktreeRoot(worktreeRoot)
	if err != nil {
		return err
	}
	_, err = run(mainRoot, "worktree", "remove", worktreeRoot)
	if err != nil {
		// Retry with --force to handle dirty worktrees.
		_, err = run(mainRoot, "worktree", "remove", "--force", worktreeRoot)
	}
	return err
}

// CreateWorktree creates a new linked worktree at destPath on the given branch.
// Tries three strategies in order:
//  1. Create a new branch and worktree (-b)
//  2. Attach to an existing branch (no -b)
//  3. Force-attach even if the branch is already checked out elsewhere (--force)
func CreateWorktree(repoPath, branch, destPath string) error {
	if _, err := run(repoPath, "worktree", "add", "-b", branch, destPath); err == nil {
		return nil
	}
	if _, err := run(repoPath, "worktree", "add", destPath, branch); err == nil {
		return nil
	}
	// Branch is already checked out in another worktree — allow it with --force.
	_, err := run(repoPath, "worktree", "add", "--force", destPath, branch)
	return err
}

// run executes a git command in the given working directory and returns
// trimmed stdout. On failure the error includes stderr for actionable messages.
func run(dir string, args ...string) (string, error) {
	var stdout, stderr bytes.Buffer
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			return "", err
		}
		return "", fmt.Errorf("%s", msg)
	}
	return strings.TrimSpace(stdout.String()), nil
}
