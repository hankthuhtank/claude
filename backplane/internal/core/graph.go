package core

import (
	"fmt"
	"sort"
)

// TopoSort orders nodes so every node comes after the nodes it depends on.
// deps maps node -> dependencies. Nodes with no ordering constraint keep the
// order given in `order` so plans are stable and readable.
func TopoSort(order []string, deps map[string][]string) ([]string, error) {
	index := map[string]int{}
	for i, n := range order {
		index[n] = i
	}
	indeg := map[string]int{}
	rev := map[string][]string{}
	for _, n := range order {
		indeg[n] += 0
		for _, d := range deps[n] {
			if _, ok := index[d]; !ok {
				return nil, fmt.Errorf("%q depends on unknown %q", n, d)
			}
			indeg[n]++
			rev[d] = append(rev[d], n)
		}
	}
	var ready []string
	for _, n := range order {
		if indeg[n] == 0 {
			ready = append(ready, n)
		}
	}
	var out []string
	for len(ready) > 0 {
		sort.SliceStable(ready, func(i, j int) bool { return index[ready[i]] < index[ready[j]] })
		n := ready[0]
		ready = ready[1:]
		out = append(out, n)
		for _, m := range rev[n] {
			indeg[m]--
			if indeg[m] == 0 {
				ready = append(ready, m)
			}
		}
	}
	if len(out) != len(order) {
		var stuck []string
		for _, n := range order {
			if indeg[n] > 0 {
				stuck = append(stuck, n)
			}
		}
		return nil, fmt.Errorf("dependency cycle between: %v", stuck)
	}
	return out, nil
}

// Downstream returns every component that depends (directly or not) on the
// given component through the blueprint's links. Links point in the direction
// data flows (Stripe -> Worker), so if the Worker breaks, everything the
// Worker feeds breaks too, and so does everything that feeds the Worker for
// the purpose of that flow. We treat a component as affected when it sits on
// any path that passes through the broken component.
func (b *Blueprint) Downstream(component string) []string {
	out := map[string]bool{}
	var walk func(string)
	walk = func(c string) {
		for _, l := range b.Links {
			if l.From == c && !out[l.To] {
				out[l.To] = true
				walk(l.To)
			}
		}
	}
	walk(component)
	keys := make([]string, 0, len(out))
	for k := range out {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// ImpactOf explains, in plain English, what breaks when a component or a
// link fails and what keeps working. Components and links list the features
// they carry (Breaks); when a blueprint does not say, the architecture graph
// is walked instead. It powers the "Affected / Unaffected" block of every
// diagnosis.
func (b *Blueprint) ImpactOf(componentOrLink string) (affected, unaffected []string) {
	if b.hasBreaks() {
		hit := map[string]bool{}
		if l := b.linkByKey(componentOrLink); l != nil {
			for _, x := range l.Breaks {
				hit[x] = true
			}
			if len(l.Breaks) == 0 {
				if c := b.ComponentByKey(l.To); c != nil {
					for _, x := range c.Breaks {
						hit[x] = true
					}
				}
			}
		} else if c := b.ComponentByKey(componentOrLink); c != nil {
			for _, x := range c.Breaks {
				hit[x] = true
			}
		}
		return b.splitFeatures(hit)
	}
	broken := map[string]bool{}
	if l := b.linkByKey(componentOrLink); l != nil {
		broken[l.To] = true
		for _, d := range b.Downstream(l.To) {
			broken[d] = true
		}
	} else {
		broken[componentOrLink] = true
		for _, d := range b.Downstream(componentOrLink) {
			broken[d] = true
		}
	}
	seenA, seenU := map[string]bool{}, map[string]bool{}
	for _, c := range b.Components {
		if c.External {
			continue
		}
		label := c.Role
		if label == "" {
			label = c.Label
		}
		if broken[c.Key] {
			if !seenA[label] {
				affected = append(affected, label)
				seenA[label] = true
			}
		} else if !seenU[label] {
			unaffected = append(unaffected, label)
			seenU[label] = true
		}
	}
	return affected, unaffected
}

// ImpactOfProvider lists features that break when a whole provider is down.
func (b *Blueprint) ImpactOfProvider(provider string) (affected, unaffected []string) {
	hit := map[string]bool{}
	for _, c := range b.Components {
		if c.Provider == provider {
			for _, x := range c.Breaks {
				hit[x] = true
			}
		}
	}
	return b.splitFeatures(hit)
}

func (b *Blueprint) hasBreaks() bool {
	for _, c := range b.Components {
		if len(c.Breaks) > 0 {
			return true
		}
	}
	return false
}

// splitFeatures returns hit features in blueprint order and every other
// feature as unaffected.
func (b *Blueprint) splitFeatures(hit map[string]bool) (affected, unaffected []string) {
	seen := map[string]bool{}
	for _, c := range b.Components {
		for _, x := range c.Breaks {
			if seen[x] {
				continue
			}
			seen[x] = true
			if hit[x] {
				affected = append(affected, x)
			} else {
				unaffected = append(unaffected, x)
			}
		}
	}
	for _, l := range b.Links {
		for _, x := range l.Breaks {
			if !seen[x] && hit[x] {
				seen[x] = true
				affected = append(affected, x)
			}
		}
	}
	return affected, unaffected
}

func (b *Blueprint) linkByKey(key string) *LinkSpec {
	for i := range b.Links {
		if b.Links[i].Key == key {
			return &b.Links[i]
		}
	}
	return nil
}

// LinkByKey is the exported form used by the verification engine.
func (b *Blueprint) LinkByKey(key string) *LinkSpec { return b.linkByKey(key) }

// ValidateGraph checks resource dependencies and links for mistakes before a
// plan is made.
func (b *Blueprint) ValidateGraph() error {
	keys := make([]string, 0, len(b.Resources))
	deps := map[string][]string{}
	seen := map[string]bool{}
	for _, r := range b.Resources {
		if seen[r.Key] {
			return fmt.Errorf("resource key %q is used twice", r.Key)
		}
		seen[r.Key] = true
		keys = append(keys, r.Key)
		deps[r.Key] = r.DependsOn
	}
	if _, err := TopoSort(keys, deps); err != nil {
		return err
	}
	comps := map[string]bool{}
	for _, c := range b.Components {
		comps[c.Key] = true
	}
	for _, l := range b.Links {
		if !comps[l.From] || !comps[l.To] {
			return fmt.Errorf("link %q connects unknown components %q -> %q", l.Key, l.From, l.To)
		}
	}
	return nil
}
