package cli

import (
	"github.com/spf13/cobra"
)

// completionLong is the help of `brooom completion`. Cobra's generated text
// only lives on the per-shell subcommands, so the install steps of all four
// shells are collected here where `brooom completion --help` and the CLI
// reference show them together.
const completionLong = `Generate the autocompletion script for brooom for the given shell.

The scripts complete commands, flags and flag values (detector names, output
formats, presets, trash strategies), session ids for 'undo' and 'sessions' and
the configured roots for 'roots remove', each with a short description where
the shell supports it. Completion never scans, writes or contacts the network.

Bash (needs the bash-completion package):

  Load it in the current session:
    source <(brooom completion bash)

  Install it permanently:
    Linux (per user, no root needed):
      mkdir -p ~/.local/share/bash-completion/completions
      brooom completion bash > ~/.local/share/bash-completion/completions/brooom
    Linux (system-wide, needs root): brooom completion bash | sudo tee /etc/bash_completion.d/brooom >/dev/null
    macOS (Homebrew): brooom completion bash > "$(brew --prefix)/etc/bash_completion.d/brooom"

Zsh:

  Completion must be enabled once; add this to ~/.zshrc if it is not yet:
    autoload -U compinit; compinit

  Install the script into a directory on your fpath (before compinit runs):
    brooom completion zsh > "${fpath[1]}/_brooom"
  Homebrew users can use "$(brew --prefix)/share/zsh/site-functions/_brooom".

Fish:

  Load it in the current session:
    brooom completion fish | source

  Install it permanently:
    brooom completion fish > ~/.config/fish/completions/brooom.fish

PowerShell:

  Load it in the current session:
    brooom completion powershell | Out-String | Invoke-Expression

  To load it in every session, add the same line to your profile
  (the path is in $PROFILE; create the file if it does not exist).

Start a new shell after installing a script.`

// completionBashLong replaces cobra's generic help of `completion bash`, whose
// system-wide install line redirects into /etc without root and therefore
// fails for a normal user. It matches the instructions of the parent command.
const completionBashLong = `Generate the autocompletion script for the bash shell.

This script depends on the 'bash-completion' package. If it is not installed
already, install it with your OS's package manager.

Load it in the current session:

  source <(brooom completion bash)

Install it permanently:

  Linux (per user, no root needed):
    mkdir -p ~/.local/share/bash-completion/completions
    brooom completion bash > ~/.local/share/bash-completion/completions/brooom

  Linux (system-wide, needs root):
    brooom completion bash | sudo tee /etc/bash_completion.d/brooom >/dev/null

  macOS (Homebrew):
    brooom completion bash > "$(brew --prefix)/etc/bash_completion.d/brooom"

Start a new shell after installing the script.`

// completionExamples are the per-shell example blocks of the completion
// command and its subcommands.
var completionExamples = map[string]string{
	"": `  brooom completion bash > ~/.local/share/bash-completion/completions/brooom
  brooom completion fish > ~/.config/fish/completions/brooom.fish
  brooom completion powershell | Out-String | Invoke-Expression`,
	"bash": `  source <(brooom completion bash)
  brooom completion bash > ~/.local/share/bash-completion/completions/brooom
  brooom completion bash | sudo tee /etc/bash_completion.d/brooom >/dev/null`,
	"zsh": `  brooom completion zsh > "${fpath[1]}/_brooom"
  brooom completion zsh --no-descriptions`,
	"fish": `  brooom completion fish | source
  brooom completion fish > ~/.config/fish/completions/brooom.fish`,
	"powershell": `  brooom completion powershell | Out-String | Invoke-Expression
  brooom completion powershell > brooom.ps1`,
}

// customizeCompletionCmd creates cobra's default completion command and
// replaces its help with Brooom's install instructions and examples. The
// command stays visible (descriptions are on by default): shell completion is
// a documented feature, not an implementation detail.
func customizeCompletionCmd(root *cobra.Command) {
	root.CompletionOptions.HiddenDefaultCmd = false
	root.InitDefaultCompletionCmd()
	for _, c := range root.Commands() {
		if c.Name() != "completion" {
			continue
		}
		c.Long = completionLong
		// Without an argument check and a RunE cobra treats the group as
		// help-only and exits 0 for `brooom completion bogus`.
		c.Args = cobra.NoArgs
		c.RunE = groupRunE
		c.Example = completionExamples[""]
		for _, sub := range c.Commands() {
			if sub.Name() == "bash" {
				sub.Long = completionBashLong
			}
			sub.Example = completionExamples[sub.Name()]
			sub.RunE = completionScriptRunE(root, sub.Name())
		}
	}
}

// completionScriptRunE generates the script of one shell. Cobra's own runner
// binds the output stream when the command is created, before the root's
// output is configured, so it would ignore SetOut; writing to
// cmd.OutOrStdout() here keeps the scripts testable and redirectable.
func completionScriptRunE(root *cobra.Command, shell string) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		out := cmd.OutOrStdout()
		withDescriptions := true
		if f := cmd.Flags().Lookup("no-descriptions"); f != nil {
			withDescriptions = f.Value.String() != "true"
		}
		switch shell {
		case "bash":
			return root.GenBashCompletionV2(out, withDescriptions)
		case "zsh":
			if withDescriptions {
				return root.GenZshCompletion(out)
			}
			return root.GenZshCompletionNoDesc(out)
		case "fish":
			return root.GenFishCompletion(out, withDescriptions)
		default:
			if withDescriptions {
				return root.GenPowerShellCompletionWithDesc(out)
			}
			return root.GenPowerShellCompletion(out)
		}
	}
}
