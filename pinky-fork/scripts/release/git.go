package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// git runs git in dir and returns its output, trimmed.
func git(dir string, args ...string) (string, error) {
	return gitInput(dir, "", args...)
}

// gitInput runs git in dir with input on its standard input.
func gitInput(dir, input string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if input != "" {
		cmd.Stdin = strings.NewReader(input)
	}
	var out, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), msg)
	}
	return strings.TrimSpace(out.String()), nil
}

// gitPassthrough runs git in dir with its output on ours, for what the
// person should see, such as a push.
func gitPassthrough(dir string, args ...string) error {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return nil
}

// repoRoot returns the top of the git checkout around the working directory.
func repoRoot() (string, error) {
	return git(".", "rev-parse", "--show-toplevel")
}

// localTags returns the tags of the checkout that look like releases.
func localTags(dir string) ([]string, error) {
	out, err := git(dir, "tag", "--list", "v*")
	if err != nil {
		return nil, err
	}
	return strings.Fields(out), nil
}

// remoteTags returns the release-like tags of a remote, waiting at most a
// little while for it.
func remoteTags(dir, remote string) ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "ls-remote", "--tags", "--refs", remote, "refs/tags/v*")
	cmd.Dir = dir
	// Never wait for a password prompt.
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	var out, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("git ls-remote %s: %s", remote, msg)
	}
	var tags []string
	for _, line := range strings.Split(out.String(), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 {
			tags = append(tags, strings.TrimPrefix(fields[1], "refs/tags/"))
		}
	}
	return tags, nil
}
