package groupmgmt

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/pfisterer/role-provider-service/internal/common"
)

// BrowsedGroup is a group as the group pages list it: where it comes from, whether the caller may change it, and how many groups it holds, so a tree knows what can be opened.
type BrowsedGroup struct {
	common.Group
	// Editable: a group of the local source the caller maintains. Groups of sync sources are read-only here; they change at their source. A group the caller may only look at (see Service.readableGroup) is listed read-only too.
	Editable bool `json:"editable"`
	// ChildGroups counts the groups entered in this one.
	ChildGroups int `json:"child_groups"`
}

// GroupPage is one page of a group listing.
type GroupPage struct {
	Items []BrowsedGroup `json:"items"`
	Total int            `json:"total"`
}

// BrowseQuery says what to list: the way down to PathTo; or the groups in Parent; or those matching Query, optionally of one Source; or the top of a Source's tree — its groups no other group of that source contains.
type BrowseQuery struct {
	Query  string
	Source string
	Parent string
	PathTo string
	Limit  int
	Offset int
}

// Browse lists groups for the group pages. Admins see every source; everybody else the groups they may look at — the local groups they maintain, the groups they view and every group nested in one of them, editable only where they maintain it —, searched by Query, whatever else is asked.
func (s *Service) Browse(ctx context.Context, caller Caller, q BrowseQuery) (*GroupPage, error) {
	if !caller.Admin {
		groups, managed, err := s.readableGroups(ctx, caller, q.Query)
		if err != nil {
			return nil, err
		}
		return s.page(ctx, groups, nil, q, func(g common.Group) bool { return managed[g.ID] })
	}

	ctx, cancel := s.ctx(ctx)
	defer cancel()
	all, err := s.allGroups(ctx)
	if err != nil {
		return nil, err
	}
	edges, err := s.store.GroupEdges(ctx)
	if err != nil {
		return nil, err
	}
	byID := make(map[string]common.Group, len(all))
	for _, g := range all {
		byID[g.ID] = g
	}
	children := map[string][]string{}
	parents := map[string][]string{}
	for _, e := range edges {
		children[e[0]] = append(children[e[0]], e[1])
		parents[e[1]] = append(parents[e[1]], e[0])
	}

	var out []common.Group
	switch {
	case q.PathTo != "":
		target := strings.TrimPrefix(q.PathTo, common.GroupPrefix)
		g, ok := byID[target]
		if !ok {
			return nil, fmt.Errorf("%w: group '%s'", ErrNotFound, q.PathTo)
		}
		for _, id := range pathDown(g, byID, parents) {
			out = append(out, byID[id])
		}
	case q.Parent != "":
		parent := strings.TrimPrefix(q.Parent, common.GroupPrefix)
		if _, ok := byID[parent]; !ok {
			return nil, fmt.Errorf("%w: group '%s'", ErrNotFound, q.Parent)
		}
		for _, id := range children[parent] {
			if g, ok := byID[id]; ok {
				out = append(out, g)
			}
		}
		slices.SortFunc(out, func(a, b common.Group) int { return strings.Compare(a.ID, b.ID) })
	case strings.TrimSpace(q.Query) != "":
		hits, err := s.search(ctx, q.Query)
		if err != nil {
			return nil, err
		}
		for _, g := range hits {
			if q.Source == "" || g.SourceName == q.Source {
				out = append(out, g)
			}
		}
	case q.Source != "":
		// The top of a source's tree: a group a group of the same source contains is listed under that one.
		for _, g := range all {
			if g.SourceName != q.Source {
				continue
			}
			nested := slices.ContainsFunc(parents[g.ID], func(p string) bool { return byID[p].SourceName == q.Source })
			if !nested {
				out = append(out, g)
			}
		}
	default:
		for _, g := range all {
			if g.SourceID != nil && *g.SourceID == s.localID {
				out = append(out, g)
			}
		}
	}
	return s.page(ctx, out, children, q, func(g common.Group) bool { return s.isLocal(&g) })
}

// pathDown is the shortest way from the top of a group's source tree down to the group, as the tree shows it: through groups of the same source, ending with the group itself.
func pathDown(g common.Group, byID map[string]common.Group, parents map[string][]string) []string {
	sameSource := func(id string) []string {
		var out []string
		for _, p := range parents[id] {
			if byID[p].SourceName == g.SourceName {
				out = append(out, p)
			}
		}
		slices.Sort(out)
		return out
	}
	// Breadth-first upwards: the first group without a parent of its source is the top.
	next := map[string]string{g.ID: ""}
	queue := []string{g.ID}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		ups := sameSource(id)
		if len(ups) == 0 {
			path := []string{}
			for at := id; at != ""; at = next[at] {
				path = append(path, at)
			}
			return path
		}
		for _, p := range ups {
			if _, seen := next[p]; !seen {
				next[p] = id
				queue = append(queue, p)
			}
		}
	}
	return []string{g.ID}
}

// page cuts one page out of the groups and says of each whether the caller may change it (editable) and how many groups it holds; children nil means they are counted here.
func (s *Service) page(ctx context.Context, groups []common.Group, children map[string][]string, q BrowseQuery, editable func(common.Group) bool) (*GroupPage, error) {
	if children == nil {
		edges, err := s.store.GroupEdges(ctx)
		if err != nil {
			return nil, err
		}
		children = map[string][]string{}
		for _, e := range edges {
			children[e[0]] = append(children[e[0]], e[1])
		}
	}
	total := len(groups)
	start := min(max(q.Offset, 0), total)
	end := total
	if q.Limit > 0 {
		end = min(start+q.Limit, total)
	}
	items := make([]BrowsedGroup, 0, end-start)
	for _, g := range groups[start:end] {
		items = append(items, BrowsedGroup{Group: g, Editable: editable(g), ChildGroups: len(children[g.ID])})
	}
	return &GroupPage{Items: items, Total: total}, nil
}

// allGroups is every group with its source's name, without the entries the search adds for each relation.
func (s *Service) allGroups(ctx context.Context) ([]common.Group, error) {
	return s.search(ctx, "")
}

// search answers a query from the cache where there is one, leaving out the relation entries ("group:x#dozent"): browsing is per group.
func (s *Service) search(ctx context.Context, query string) ([]common.Group, error) {
	var groups []common.Group
	if s.cache != nil {
		groups = s.cache.Search(query, 0)
	} else {
		var err error
		if groups, err = s.store.ListGroups(ctx, query, nil, 0); err != nil {
			return nil, err
		}
		groups = s.withSourceNames(ctx, groups)
	}
	return slices.DeleteFunc(groups, func(g common.Group) bool { return strings.Contains(g.ID, common.RelationSeparator) }), nil
}

// SourceSummary is a source as the group pages show it: the local one first, the sync sources read-only.
type SourceSummary struct {
	Name string `json:"name"`
	Type string `json:"type"`
	// Local: groups of this source are maintained here; the others come from their sync.
	Local          bool       `json:"local"`
	Groups         int        `json:"groups"`
	LastSyncedAt   *time.Time `json:"last_synced_at,omitempty"`
	LastSyncStatus string     `json:"last_sync_status,omitempty"`
}

// Sources lists every source with its number of groups, the local one first. Admins only.
func (s *Service) Sources(ctx context.Context, caller Caller) ([]SourceSummary, error) {
	if !caller.Admin {
		return nil, fmt.Errorf("%w: only admins see every source", ErrForbidden)
	}
	ctx, cancel := s.ctx(ctx)
	defer cancel()
	sources, err := s.store.ListSources(ctx)
	if err != nil {
		return nil, err
	}
	all, err := s.allGroups(ctx)
	if err != nil {
		return nil, err
	}
	count := map[string]int{}
	for _, g := range all {
		count[g.SourceName]++
	}
	out := make([]SourceSummary, 0, len(sources))
	for _, src := range sources {
		out = append(out, SourceSummary{
			Name: src.Name, Type: src.Type, Local: src.ID == s.localID, Groups: count[src.Name],
			LastSyncedAt: src.LastSyncedAt, LastSyncStatus: src.LastSyncStatus,
		})
	}
	slices.SortFunc(out, func(a, b SourceSummary) int {
		if a.Local != b.Local {
			if a.Local {
				return -1
			}
			return 1
		}
		return cmp.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
	})
	return out, nil
}

// readableGroup returns a group the caller may look at, and whether they maintain it: any group for an admin (maintains: true, though of a sync source's group only the viewers can be changed); a local group they manage; else a group they may only look at (see mayView) — of any source, so a course group a sync brings can be read below the local group it is nested in. Otherwise the refusal a maintainer gets (see localGroup).
func (s *Service) readableGroup(ctx context.Context, caller Caller, token string) (*common.Group, bool, error) {
	_, id, err := parseGroupToken(token)
	if err != nil {
		return nil, false, err
	}
	g, err := s.getGroup(ctx, id, token)
	if err != nil {
		return nil, false, err
	}
	if caller.Admin {
		return g, true, nil
	}
	if s.isLocal(g) {
		ok, err := s.mayManage(ctx, caller, g.ID)
		if err != nil {
			return nil, false, err
		}
		if ok {
			return g, true, nil
		}
	}
	ok, err := s.mayView(ctx, caller, g.ID)
	if err != nil {
		return nil, false, err
	}
	if ok {
		return g, false, nil
	}
	if _, err := s.localGroup(ctx, caller, token); err != nil {
		return nil, false, err
	}
	return nil, false, fmt.Errorf("%w: group '%s' is not one you maintain", ErrForbidden, token)
}

// grants are the groups the caller maintains (managed) or is a viewer of (viewed), directly through one of their tokens. A manager entry counts only on a local group, the only ones maintained here; a viewer entry on a group of any source, since admins set viewers on a sync source's groups too (see viewersGroup).
func (s *Service) grants(ctx context.Context, caller Caller) (managed, viewed map[string]bool, err error) {
	managedIDs, err := s.store.ManagedGroups(ctx, caller.Tokens)
	if err != nil {
		return nil, nil, err
	}
	viewedIDs, err := s.store.ViewedGroups(ctx, caller.Tokens)
	if err != nil {
		return nil, nil, err
	}
	managed, viewed = map[string]bool{}, map[string]bool{}
	if len(managedIDs)+len(viewedIDs) == 0 {
		return managed, viewed, nil
	}
	owners, err := s.store.GroupOwners(ctx, append(slices.Clone(managedIDs), viewedIDs...))
	if err != nil {
		return nil, nil, err
	}
	local := func(id string) bool { o := owners[id]; return o != nil && *o == s.localID }
	for _, id := range managedIDs {
		if local(id) {
			managed[id] = true
		}
	}
	for _, id := range viewedIDs {
		// Only groups that exist: a row whose group is gone grants nothing.
		if _, ok := owners[id]; ok {
			viewed[id] = true
		}
	}
	return managed, viewed, nil
}

// groupTree is the live nesting of groups both ways, from GroupEdges: an expired entry of a group in a group nests nothing.
func (s *Service) groupTree(ctx context.Context) (children, parents map[string][]string, err error) {
	edges, err := s.store.GroupEdges(ctx)
	if err != nil {
		return nil, nil, err
	}
	children, parents = map[string][]string{}, map[string][]string{}
	for _, e := range edges {
		children[e[0]] = append(children[e[0]], e[1])
		parents[e[1]] = append(parents[e[1]], e[0])
	}
	return children, parents, nil
}

// reach is every group reachable from the start groups along next, the start groups included. Each group is visited once, so a cycle — refused for groups maintained here, but a sync could bring one — ends the walk instead of looping, and the walk is bounded by the number of groups.
func reach(start []string, next map[string][]string) map[string]bool {
	seen := make(map[string]bool, len(start))
	queue := make([]string, 0, len(start))
	for _, id := range start {
		if !seen[id] {
			seen[id] = true
			queue = append(queue, id)
		}
	}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		for _, n := range next[id] {
			if !seen[n] {
				seen[n] = true
				queue = append(queue, n)
			}
		}
	}
	return seen
}

// mayView reports whether the caller may look at a group's members without maintaining it: they are a viewer of it, or a manager of a local group or a viewer of any group it is nested in, directly or further down — through any entry of the group in that group, also one naming a relation ("group:wwi24#studierende"). Reading is all that is passed down: managing a group lets nobody change the groups nested in it.
func (s *Service) mayView(ctx context.Context, caller Caller, groupID string) (bool, error) {
	managed, viewed, err := s.grants(ctx, caller)
	if err != nil || len(managed)+len(viewed) == 0 {
		return false, err
	}
	_, parents, err := s.groupTree(ctx)
	if err != nil {
		return false, err
	}
	for id := range reach([]string{groupID}, parents) {
		if managed[id] || viewed[id] {
			return true, nil
		}
	}
	return false, nil
}

// readableGroups lists, for somebody not an admin, the groups they may look at — the local groups they maintain, the groups of any source they view, and every group nested in one of them (see mayView) —, narrowed by a search query, and the ones of them they maintain.
func (s *Service) readableGroups(ctx context.Context, caller Caller, query string) ([]common.Group, map[string]bool, error) {
	ctx, cancel := s.ctx(ctx)
	defer cancel()
	managed, viewed, err := s.grants(ctx, caller)
	if err != nil {
		return nil, nil, err
	}
	if len(managed)+len(viewed) == 0 {
		return []common.Group{}, managed, nil
	}
	children, _, err := s.groupTree(ctx)
	if err != nil {
		return nil, nil, err
	}
	roots := make([]string, 0, len(managed)+len(viewed))
	for id := range managed {
		roots = append(roots, id)
	}
	for id := range viewed {
		roots = append(roots, id)
	}
	readable := reach(roots, children)
	candidates, err := s.search(ctx, query)
	if err != nil {
		return nil, nil, err
	}
	out := []common.Group{}
	for _, g := range candidates {
		if readable[g.ID] {
			out = append(out, g)
		}
	}
	return out, managed, nil
}

// ViewsAny reports whether the caller is a viewer of at least one group: whether the group pages have something to show them even where they maintain nothing.
func (s *Service) ViewsAny(ctx context.Context, caller Caller) (bool, error) {
	ctx, cancel := s.ctx(ctx)
	defer cancel()
	_, viewed, err := s.grants(ctx, caller)
	return len(viewed) > 0, err
}
