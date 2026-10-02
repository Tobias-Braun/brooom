package cli

// scopeFlags are the flags that decide where the invocation worked and which
// config it used, spelled for undo, which takes the scope as --path; the
// printed undo command has to repeat them to restore in the same scope.
func (a *app) scopeFlags() []string {
	out := a.configFlag()
	if a.flags.path != "" {
		out = append(out, "--path", a.quote(a.flags.path))
	}
	return out
}

// configFlag repeats --config when the invocation named a config file.
func (a *app) configFlag() []string {
	if a.flags.configPath == "" {
		return nil
	}
	return []string{"--config", a.quote(a.flags.configPath)}
}
