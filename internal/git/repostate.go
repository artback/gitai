package git

import (
	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing/object"
)

type repoState struct {
	repo     *git.Repository
	worktree *git.Worktree
	head     *object.Commit // Nil if initial commit
	root     string
}

func (s *Service) getContext() (*repoState, error) {
	repo, wt, root, err := getRepo()
	if err != nil {
		return nil, err
	}
	state := &repoState{repo: repo, worktree: wt, root: root}

	if head, err := repo.Head(); err == nil {
		state.head, _ = repo.CommitObject(head.Hash())
	}
	return state, nil
}
