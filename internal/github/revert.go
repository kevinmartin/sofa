package github

import (
	"context"
	"errors"
)

// VerifiedRevert checks a strict, complete inverse of one merge commit's
// first-parent file changes. Ambiguous, partial, or unrelated commits are not
// called reverts. Both commits must be ancestors of the current default head.
func (c *Client) VerifiedRevert(ctx context.Context, repository, mergedSHA, revertSHA, defaultHead string) (bool, error) {
	if !lifecycleRepositoryPattern.MatchString(repository) || !lifecycleSHAPattern.MatchString(mergedSHA) || !lifecycleSHAPattern.MatchString(revertSHA) || !lifecycleSHAPattern.MatchString(defaultHead) || mergedSHA == revertSHA {
		return false, errors.New("invalid revert identity")
	}
	mergedOnDefault, err := c.IsAncestor(ctx, repository, mergedSHA, defaultHead)
	if err != nil || !mergedOnDefault {
		return false, err
	}
	revertOnDefault, err := c.IsAncestor(ctx, repository, revertSHA, defaultHead)
	if err != nil || !revertOnDefault {
		return false, err
	}
	mergedBeforeRevert, err := c.IsAncestor(ctx, repository, mergedSHA, revertSHA)
	if err != nil || !mergedBeforeRevert {
		return false, err
	}
	merged, err := c.commit(ctx, repository, mergedSHA)
	if err != nil {
		return false, err
	}
	revert, err := c.commit(ctx, repository, revertSHA)
	if err != nil {
		return false, err
	}
	if len(merged.Parents) < 1 || merged.Tree.SHA == "" || revert.Tree.SHA == "" {
		return false, errors.New("revert commit ancestry unavailable")
	}
	if len(revert.Parents) != 1 {
		return false, nil
	}
	mergedParent, err := c.commit(ctx, repository, merged.Parents[0].SHA)
	if err != nil {
		return false, err
	}
	revertParent, err := c.commit(ctx, repository, revert.Parents[0].SHA)
	if err != nil {
		return false, err
	}
	before, err := c.fileTree(ctx, repository, mergedParent.Tree.SHA)
	if err != nil {
		return false, err
	}
	after, err := c.fileTree(ctx, repository, merged.Tree.SHA)
	if err != nil {
		return false, err
	}
	revertBefore, err := c.fileTree(ctx, repository, revertParent.Tree.SHA)
	if err != nil {
		return false, err
	}
	revertAfter, err := c.fileTree(ctx, repository, revert.Tree.SHA)
	if err != nil {
		return false, err
	}
	changed := map[string]bool{}
	for path, entry := range before {
		if after[path] != entry {
			changed[path] = true
		}
	}
	for path, entry := range after {
		if before[path] != entry {
			changed[path] = true
		}
	}
	if len(changed) == 0 || len(changed) > 100 {
		return false, nil
	}
	for path := range changed {
		if revertBefore[path] != after[path] || revertAfter[path] != before[path] {
			return false, nil
		}
	}
	for path, entry := range revertBefore {
		if revertAfter[path] != entry && !changed[path] {
			return false, nil
		}
	}
	for path, entry := range revertAfter {
		if revertBefore[path] != entry && !changed[path] {
			return false, nil
		}
	}
	return true, nil
}

// fileTree indexes non-directory tree entries by path for exact revert comparison.
// It propagates tree retrieval errors and rejects incomplete or duplicate entries.
func (c *Client) fileTree(ctx context.Context, repository, sha string) (map[string]treeEntry, error) {
	entries, err := c.tree(ctx, repository, sha)
	if err != nil {
		return nil, err
	}
	files := make(map[string]treeEntry, len(entries))
	for _, entry := range entries {
		if entry.Type == "tree" {
			continue
		}
		if entry.Path == "" || entry.SHA == "" || entry.Mode == "" {
			return nil, errors.New("revert tree entry unavailable")
		}
		if _, duplicate := files[entry.Path]; duplicate {
			return nil, errors.New("revert tree entry duplicated")
		}
		files[entry.Path] = entry
	}
	return files, nil
}
