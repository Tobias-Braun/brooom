package findings

// RefusedBranchDelete is the suggested action of a branch finding whose name
// the delete-branch action refuses (see gitx.RefusedBranchName). The finding
// stays visible but is not actionable: the reason says why and gives the quoted
// `git update-ref -d` command the user can run by hand, which takes the full
// ref and therefore cannot mistake the name for an option or a revision.
func RefusedBranchDelete(name, reason string) SuggestedAction {
	return SuggestedAction{
		Type:   ActionNone,
		Reason: reason + "; brooom will not delete it, remove it manually with: git update-ref -d " + Quote("refs/heads/"+name),
	}
}
