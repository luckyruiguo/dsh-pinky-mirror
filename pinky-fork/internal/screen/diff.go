package screen

// Diff compares two screens of the same device, after refs were assigned to
// both. It returns the lines to send after an action, or full=true when the
// whole screen should be sent instead: a different app, size, orientation or
// lock state, or a diff longer than the screen itself.
//
// Lines start with "+ " (added), "- " (removed) or "~ " (changed). Events
// on next are always included, first, since they are new by definition.
func Diff(prev, next *Screen) (lines []string, full bool) {
	if prev == nil || next == nil {
		return nil, true
	}
	if prev.App.ID != next.App.ID || prev.Width != next.Width || prev.Height != next.Height ||
		prev.Orientation != next.Orientation || prev.Locked != next.Locked {
		return nil, true
	}
	for _, ev := range next.Events {
		lines = append(lines, ev.Line())
	}
	if prev.Header() != next.Header() {
		lines = append(lines, "~ "+next.Header())
	}

	oldByRef := map[int]Element{}
	oldOCR := map[string]bool{}
	for _, e := range prev.Elements {
		if e.Source == SourceOCR {
			oldOCR[e.Line()] = true
		} else if e.Ref != 0 {
			oldByRef[e.Ref] = e
		}
	}
	seen := map[int]bool{}
	newOCR := map[string]bool{}
	for _, e := range next.Elements {
		if e.Source == SourceOCR {
			l := e.Line()
			newOCR[l] = true
			if !oldOCR[l] {
				lines = append(lines, "+ "+l)
			}
			continue
		}
		seen[e.Ref] = true
		old, ok := oldByRef[e.Ref]
		switch {
		case !ok:
			lines = append(lines, "+ "+e.Line())
		case old.Line() != e.Line():
			lines = append(lines, "~ "+e.Line())
		}
	}
	for _, e := range prev.Elements {
		if e.Source == SourceOCR {
			if l := e.Line(); !newOCR[l] {
				lines = append(lines, "- "+l)
			}
			continue
		}
		if e.Ref != 0 && !seen[e.Ref] {
			lines = append(lines, "- "+e.Line())
		}
	}
	if len(lines) > len(next.Elements)+len(next.Events) {
		return nil, true
	}
	return lines, false
}
