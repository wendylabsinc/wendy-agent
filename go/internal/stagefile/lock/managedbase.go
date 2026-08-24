package lock

// ManagedBaseRefreshes returns the image refs whose digest must be refreshed
// because Wendy's catalog selection or revision changed. A newly managed base
// refreshes too: an old explicit from: pin for the same ref must not silently
// become the starting point of a maintained channel.
func ManagedBaseRefreshes(existing *File, desired map[string]ManagedBase) map[string]bool {
	refresh := map[string]bool{}
	for name, selected := range desired {
		if existing == nil {
			continue // the ref is missing and Resolve will fetch it normally
		}
		pinned, ok := existing.ManagedBases[name]
		if !ok || pinned != selected {
			refresh[selected.Ref] = true
		}
	}
	return refresh
}
