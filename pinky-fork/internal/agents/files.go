package agents

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// target is where a change to path is written: the file a symbolic link
// points at (dotfiles kept in a repository), so the link stays a link.
func target(path string) string {
	if real, err := filepath.EvalSymlinks(path); err == nil {
		return real
	}
	return path
}

// readFile reads a settings file; a missing one reads as empty, with ok
// false.
func readFile(path string) (data []byte, ok bool, err error) {
	data, err = os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	return data, err == nil, err
}

// writeFile replaces a file's contents through a temporary file next to it
// and a rename, so an agent that reads it meanwhile reads all of the old
// file or all of the new one. It keeps the file's mode; a new file is the
// person's alone (0600), in folders made as needed.
func writeFile(path string, data []byte) error {
	path = target(path)
	mode := fs.FileMode(0o600)
	if fi, err := os.Stat(path); err == nil {
		if !fi.Mode().IsRegular() {
			return fmt.Errorf("%s is not a file", path)
		}
		mode = fi.Mode().Perm()
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".pinky-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, werr := f.Write(data)
	if werr == nil {
		werr = f.Sync()
	}
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Chmod(tmp, mode)
	}
	if werr == nil {
		werr = os.Rename(tmp, path)
	}
	if werr != nil {
		os.Remove(tmp)
		return werr
	}
	return nil
}

// keepBackups is how many copies of an agent's files Pinky keeps.
const keepBackups = 10

// backup keeps a copy of a file as it was before Pinky changed it, in
// Pinky's own folder (dir/<agent>/<time>-<name>), and returns its path.
// Only the newest copies stay.
func backup(dir, agent, path string, data []byte, now time.Time) (string, error) {
	if dir == "" {
		return "", errors.New("no folder for backups")
	}
	folder := filepath.Join(dir, agent)
	if err := os.MkdirAll(folder, 0o700); err != nil {
		return "", err
	}
	name := now.Format("20060102-150405.000") + "-" + filepath.Base(path)
	dst := filepath.Join(folder, name)
	if err := os.WriteFile(dst, data, 0o600); err != nil {
		return "", err
	}
	entries, err := os.ReadDir(folder)
	if err == nil && len(entries) > keepBackups {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			if !e.IsDir() {
				names = append(names, e.Name())
			}
		}
		sort.Strings(names) // oldest first: the names start with the time
		for _, n := range names[:max(0, len(names)-keepBackups)] {
			os.Remove(filepath.Join(folder, n))
		}
	}
	return dst, nil
}

// skillName is the folder and the name of Pinky's skill.
const skillName = "pinky"

// skillState is how an agent's copy of Pinky's skill compares with this
// Pinky's.
type skillState int

const (
	skillMissing skillState = iota
	skillCurrent
	skillOld   // Pinky's, from another version
	skillOther // a SKILL.md that is not Pinky's: left alone
)

func skillFile(dir string) string { return filepath.Join(dir, skillName, "SKILL.md") }

func readSkill(dir, text string) skillState {
	data, err := os.ReadFile(skillFile(dir))
	switch {
	case err != nil:
		return skillMissing
	case string(data) == text:
		return skillCurrent
	case isPinkySkill(data):
		return skillOld
	}
	return skillOther
}

// isPinkySkill reports whether a SKILL.md is Pinky's: its front matter
// names it pinky.
func isPinkySkill(data []byte) bool {
	s := strings.ReplaceAll(string(data), "\r\n", "\n")
	if !strings.HasPrefix(s, "---\n") {
		return false
	}
	end := strings.Index(s[4:], "\n---")
	if end < 0 {
		return false
	}
	for _, line := range strings.Split(s[4:4+end], "\n") {
		if k, v, ok := strings.Cut(line, ":"); ok && strings.TrimSpace(k) == "name" {
			return strings.Trim(strings.TrimSpace(v), `"'`) == skillName
		}
	}
	return false
}

// installSkill writes Pinky's skill into an agent's skills folder. A
// SKILL.md there that is not Pinky's stays.
func installSkill(dir, text string) error {
	switch readSkill(dir, text) {
	case skillCurrent:
		return nil
	case skillOther:
		return fmt.Errorf("%s is not Pinky's; it stays as it is", skillFile(dir))
	}
	return writeFile(skillFile(dir), []byte(text))
}

// removeSkill takes Pinky's skill out of an agent's skills folder, and its
// folder when nothing else is in it.
func removeSkill(dir string) error {
	file := skillFile(dir)
	data, err := os.ReadFile(file)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !isPinkySkill(data) {
		return nil
	}
	if err := os.Remove(file); err != nil {
		return err
	}
	os.Remove(filepath.Dir(file)) // only when empty
	return nil
}
