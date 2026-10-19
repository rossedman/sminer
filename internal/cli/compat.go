package cli

import (
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// removalDate is when the pre-cobra command names stop working.
const removalDate = "2027-04-30"

// addCompat keeps the 0.1 command names working: scripts written against
// them keep running, with a deprecation notice on stderr naming the
// replacement and the removal date.
func addCompat(root *cobra.Command, a *app) {
	for name, build := range map[string]func(*app) *cobra.Command{
		"show":    newFindingShowCmd,
		"accept":  newFindingAcceptCmd,
		"dismiss": newFindingDismissCmd,
		"trend":   newFindingTrendCmd,
	} {
		alias := build(a)
		alias.Use = name + " " + trimUse(alias.Use)
		alias.Hidden = true
		alias.Annotations = map[string]string{deprecatedNote: "'" + name + "' is deprecated; use 'sminer finding " + name + "'. It will be removed after " + removalDate + "."}
		root.AddCommand(alias)
	}

	// --no-llm became --offline.
	for _, name := range []string{"run", "analyze"} {
		c := findSub(root, name)
		c.Flags().Bool("no-llm", false, "")
		bindFlag(c.Flags(), "no-llm", "offline")
		deprecateFlag(c, "no-llm", "--offline")
	}
}

// deprecatedNote is the annotation holding a command's deprecation notice.
// Cobra's own Deprecated field prints to stdout, which is for data only.
const deprecatedNote = "sminer/deprecated"

// deprecateFlag hides a flag and warns on stderr when it is used.
func deprecateFlag(c *cobra.Command, name, replacement string) {
	f := c.Flags().Lookup(name)
	f.Hidden = true
	_ = c.Flags().SetAnnotation(name, deprecatedNote, []string{
		"--" + name + " is deprecated; use " + replacement + ". It will be removed after " + removalDate + "."})
}

// warnDeprecated prints notices for a deprecated command or flags in use.
func (a *app) warnDeprecated(cmd *cobra.Command) {
	if note := cmd.Annotations[deprecatedNote]; note != "" {
		a.warn("%s", note)
	}
	cmd.Flags().Visit(func(f *pflag.Flag) {
		if n := f.Annotations[deprecatedNote]; len(n) == 1 {
			a.warn("%s", n[0])
		}
	})
}

func findSub(root *cobra.Command, path ...string) *cobra.Command {
	c, _, err := root.Find(path)
	if err != nil || c == root {
		panic("compat: no command " + path[len(path)-1])
	}
	return c
}

// trimUse drops the command name from a Use line, keeping its arguments.
func trimUse(use string) string {
	for i, r := range use {
		if r == ' ' {
			return use[i+1:]
		}
	}
	return ""
}
