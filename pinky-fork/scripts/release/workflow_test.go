package main

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

// workflowCache is a step of a workflow that uses actions/cache.
type workflowCache struct {
	Job, RunsOn, Step, Action, Path, Key string
	LookupOnly                           bool
}

func (c workflowCache) String() string {
	return fmt.Sprintf("%s (%s) › %s: %s, path %s, key %s", c.Job, c.RunsOn, c.Step, c.Action, c.Path, c.Key)
}

// workflowCaches lists the cache steps of a workflow file.
func workflowCaches(t *testing.T, file string) []workflowCache {
	t.Helper()
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var wf struct {
		Jobs map[string]struct {
			RunsOn string `yaml:"runs-on"`
			Steps  []struct {
				Name string            `yaml:"name"`
				Uses string            `yaml:"uses"`
				With map[string]string `yaml:"with"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(data, &wf); err != nil {
		t.Fatalf("%s: %v", file, err)
	}
	var caches []workflowCache
	for job, j := range wf.Jobs {
		for _, s := range j.Steps {
			action, _, _ := strings.Cut(s.Uses, "@")
			if action != "actions/cache" && !strings.HasPrefix(action, "actions/cache/") {
				continue
			}
			caches = append(caches, workflowCache{
				Job: job, RunsOn: j.RunsOn, Step: s.Name, Action: action,
				Path: s.With["path"], Key: s.With["key"],
				LookupOnly: s.With["lookup-only"] == "true",
			})
		}
	}
	return caches
}

// TestReleaseCaches checks that release-cache.yml makes the caches that
// release.yml restores, under the same paths and keys, in jobs of the same
// names on the same runners. A tag's run restores only caches that main's
// runs saved (or the tag's own), so releases depend on release-cache.yml
// for them, and GitHub tells caches apart by key and by a version made
// from the path and the runner's compression: a cache saved otherwise
// would be made for nothing, and every release would build FFmpeg and adb
// again with nothing failing.
func TestReleaseCaches(t *testing.T) {
	root, err := sourceRoot()
	if err != nil {
		t.Fatal(err)
	}
	release := workflowCaches(t, filepath.Join(root, ".github", "workflows", "release.yml"))
	fill := workflowCaches(t, filepath.Join(root, ".github", "workflows", "release-cache.yml"))
	if len(release) == 0 {
		t.Fatal("release.yml has no cache steps")
	}
	same := func(a, b workflowCache) bool { return a.Path == b.Path && a.Key == b.Key }
	for _, r := range release {
		looked := slices.ContainsFunc(fill, func(f workflowCache) bool {
			return same(f, r) && f.Action == "actions/cache/restore" && f.LookupOnly
		})
		saved := slices.ContainsFunc(fill, func(f workflowCache) bool {
			return same(f, r) && f.Job == r.Job && f.RunsOn == r.RunsOn && f.Action == "actions/cache" && !f.LookupOnly
		})
		if !looked || !saved {
			t.Errorf("release-cache.yml does not look up (%v) and save (%v) the cache of %v", looked, saved, r)
		}
	}
	for _, f := range fill {
		if !slices.ContainsFunc(release, func(r workflowCache) bool { return same(r, f) }) {
			t.Errorf("release.yml has no cache like %v", f)
		}
	}
}
