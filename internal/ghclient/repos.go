package ghclient

import (
	"context"
	"regexp"

	"github.com/google/go-github/v88/github"
)

// RepoFilter decides which repositories are scanned.
type RepoFilter struct {
	IncludeArchived bool
	Include         *regexp.Regexp // nil matches everything
	Exclude         *regexp.Regexp // nil excludes nothing
}

// Match reports whether repo should be scanned.
func (f RepoFilter) Match(repo *github.Repository) bool {
	if repo.GetDisabled() || (repo.GetArchived() && !f.IncludeArchived) {
		return false
	}
	name := repo.GetName()
	if f.Include != nil && !f.Include.MatchString(name) {
		return false
	}
	if f.Exclude != nil && f.Exclude.MatchString(name) {
		return false
	}
	return true
}

// ListRepos returns the names of the organization's repositories that pass filter.
func ListRepos(ctx context.Context, client *github.Client, org string, filter RepoFilter) ([]string, error) {
	var names []string
	err := Paginate(ctx,
		func(p Page) ([]*github.Repository, *github.Response, error) {
			return client.Repositories.ListByOrg(ctx, org, &github.RepositoryListByOrgOptions{
				ListOptions: github.ListOptions{PerPage: PerPage, Page: p.Num},
			})
		},
		func(r *github.Repository) {
			if filter.Match(r) {
				names = append(names, r.GetName())
			}
		},
	)
	return names, err
}
