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
	// Editable: a group of the local source the caller maintains. Groups of sync sources are read-only here; they change at their source.
	Editable bool `json:"editable"`
	// ChildGroups counts the groups entered in this one.
	ChildGroups int `json:"child_groups"`
}

// GroupPage is one page of a group listing.
type GroupPage struct {
	Items []BrowsedGroup `json:"items"`
	Total int            `json:"total"`
}

// BrowseQuery says what to list: the groups in Parent; or those matching Query, optionally of one Source; or the top of a Source's tree — its groups no other group of that source contains.
type BrowseQuery struct {
	Query  string
	Source string
	Parent string
	Limit  int
	Offset int
}

// Browse lists groups for the group pages. Admins see every source; everybody else the local groups they maintain, searched by Query, whatever else is asked.
func (s *Service) Browse(ctx context.Context, caller Caller, q BrowseQuery) (*GroupPage, error) {
	if !caller.Admin {
		groups, err := s.ManagedGroups(ctx, caller, q.Query)
		if err != nil {
			return nil, err
		}
		return s.page(ctx, groups, nil, q)
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
	return s.page(ctx, out, children, q)
}

// page cuts one page out of the groups and says of each whether the caller may change it — every local group listed is, since admins maintain all of them and everybody else is listed only theirs — and how many groups it holds; children nil means they are counted here.
func (s *Service) page(ctx context.Context, groups []common.Group, children map[string][]string, q BrowseQuery) (*GroupPage, error) {
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
		local := g.SourceID != nil && *g.SourceID == s.localID
		items = append(items, BrowsedGroup{Group: g, Editable: local, ChildGroups: len(children[g.ID])})
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

// readableGroup returns a group the caller may look at: any group for an admin, else a local group they maintain (see localGroup).
func (s *Service) readableGroup(ctx context.Context, caller Caller, token string) (*common.Group, error) {
	if !caller.Admin {
		return s.localGroup(ctx, caller, token)
	}
	_, id, err := parseGroupToken(token)
	if err != nil {
		return nil, err
	}
	return s.getGroup(ctx, id, token)
}
