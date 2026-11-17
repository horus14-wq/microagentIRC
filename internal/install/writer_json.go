package install

// The JSON writer: the one writer for every CLI whose configuration is a JSON file. It changes only the entries a
// fileSpec names and keeps every other key, its order and its number text (ordered.go).

// applyJSON adds, updates or removes mairc's entries in root. edited reports a change; ok is false when the file's
// shape cannot hold them (a key on the path holds something other than an object or array), and then nothing may be
// written.
func applyJSON(root *node, f fileSpec, binary string, uninstall bool) (edited, ok bool) {
	if f.server != nil {
		e, ok := applyEntry(root, f.server, binary, uninstall)
		if !ok {
			return false, false
		}
		edited = edited || e
	}
	if f.hooks != nil {
		var e bool
		if uninstall {
			e = removeHooks(root, f.hooks)
		} else {
			var ok bool
			if e, ok = addHooks(root, f.hooks, binary); !ok {
				return false, false
			}
		}
		edited = edited || e
	}
	return edited, true
}

// container walks keyPath from root. With create, missing objects are added; it returns nil, false when a key on
// the path holds a non-object.
func container(root *node, keyPath []string, create bool) (*node, bool) {
	at := root
	for _, key := range keyPath {
		if create {
			next, ok := at.child(key, objectNode)
			if !ok {
				return nil, false
			}
			at = next
			continue
		}
		next := at.get(key)
		if next == nil || next.kind != objectNode {
			return nil, true
		}
		at = next
	}
	return at, true
}

// applyEntry adds or updates the named entry in place (keeping any other key the user added to it, such as env), or
// removes it and the containers on its path that only it filled.
func applyEntry(root *node, e *entrySpec, binary string, uninstall bool) (edited, ok bool) {
	if uninstall {
		parent, _ := container(root, e.keyPath, false)
		if parent == nil || !parent.del(e.name) {
			return false, true
		}
		pruneEmpty(root, e.keyPath)
		return true, true
	}
	parent, ok := container(root, e.keyPath, true)
	if !ok {
		return false, false
	}
	want := e.value(binary)
	existing := parent.get(e.name)
	if existing == nil || existing.kind != objectNode {
		parent.set(e.name, want)
		return true, true
	}
	for i, key := range want.keys {
		if !want.items[i].equal(existing.get(key)) {
			existing.set(key, want.items[i])
			edited = true
		}
	}
	return edited, true
}

// pruneEmpty removes the objects on keyPath, innermost first, that are now empty.
func pruneEmpty(root *node, keyPath []string) {
	for depth := len(keyPath); depth > 0; depth-- {
		parent, _ := container(root, keyPath[:depth-1], false)
		child := parent.get(keyPath[depth-1])
		if parent == nil || child == nil || child.kind != objectNode || len(child.keys) > 0 {
			return
		}
		parent.del(keyPath[depth-1])
	}
}

// addHooks adds mairc's handler to each event (a new group), or updates an existing mairc handler in place.
func addHooks(root *node, h *hookSpec, binary string) (edited, ok bool) {
	hooks, ok := container(root, h.keyPath, true)
	if !ok {
		return false, false
	}
	want := h.handler(binary)
	for _, event := range h.events {
		groups, ok := hooks.child(event, arrayNode)
		if !ok {
			return false, false
		}
		found := false
		for _, group := range groups.items {
			handlers := group.get("hooks")
			if handlers == nil || handlers.kind != arrayNode {
				continue
			}
			for i, handler := range handlers.items {
				if !h.ours(handler) {
					continue
				}
				found = true
				if !handler.equal(want) {
					handlers.items[i] = h.handler(binary)
					edited = true
				}
			}
		}
		if !found {
			groups.items = append(groups.items, object("hooks", array(h.handler(binary))))
			edited = true
		}
	}
	return edited, true
}

// removeHooks removes every mairc handler, then each group, event array and hooks object that only it emptied.
func removeHooks(root *node, h *hookSpec) bool {
	hooks, _ := container(root, h.keyPath, false)
	if hooks == nil {
		return false
	}
	removed := false
	for _, event := range append([]string(nil), hooks.keys...) {
		groups := hooks.get(event)
		if groups == nil || groups.kind != arrayNode {
			continue
		}
		removedHere := false
		kept := groups.items[:0]
		for _, group := range groups.items {
			handlers := group.get("hooks")
			if handlers == nil || handlers.kind != arrayNode {
				kept = append(kept, group)
				continue
			}
			removedInGroup := false
			keptHandlers := handlers.items[:0]
			for _, handler := range handlers.items {
				if h.ours(handler) {
					removedInGroup = true
					continue
				}
				keptHandlers = append(keptHandlers, handler)
			}
			handlers.items = keptHandlers
			if removedInGroup {
				removed, removedHere = true, true
				if len(keptHandlers) == 0 {
					continue // the group only held ours
				}
			}
			kept = append(kept, group)
		}
		groups.items = kept
		if removedHere && len(kept) == 0 {
			hooks.del(event)
		}
	}
	if removed {
		pruneEmpty(root, h.keyPath)
	}
	return removed
}
