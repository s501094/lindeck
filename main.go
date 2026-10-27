// Command lindeck is a floating-window package manager for apt, in the style
// of plugdeck.nvim: browse and search the archive, health-scan installed
// packages, and install/remove without typing sudo (it prompts for you).
package main

import (
	"fmt"
	"os"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

const usage = `lindeck — package manager front end

usage:
  lindeck [query]            open the TUI (optionally searching for query)
  lindeck search <query>     print matching packages
  lindeck install <pkg>...   install packages (prompts for sudo)
  lindeck remove <pkg>...    remove packages (prompts for sudo)
`

func main() {
	args := os.Args[1:]
	if len(args) == 0 || !isSubcommand(args[0]) {
		runTUI(strings.Join(args, " "))
		return
	}

	switch args[0] {
	case "search":
		if len(args) < 2 {
			die("usage: lindeck search <query>")
		}
		idx, err := loadIndex()
		if err != nil {
			die(err.Error())
		}
		installed := map[string]bool{}
		for _, in := range loadInstalled() {
			installed[in.Name] = true
		}
		for _, p := range idx.Search(strings.Join(args[1:], " ")) {
			mark := " "
			if installed[p.Name] {
				mark = "✓"
			}
			fmt.Printf("%s %-30s %s\n", mark, p.Name, p.Desc)
		}
	case "install", "remove":
		if len(args) < 2 {
			die("usage: lindeck " + args[0] + " <pkg>...")
		}
		cmd := []string{"apt-get", args[0], "--"}
		if err := sudoInteractive(append(cmd, args[1:]...)); err != nil {
			os.Exit(1)
		}
	case "help", "-h", "--help":
		fmt.Print(usage)
	}
}

func isSubcommand(s string) bool {
	switch s {
	case "search", "install", "remove", "help", "-h", "--help":
		return true
	}
	return false
}

func runTUI(query string) {
	if _, err := tea.NewProgram(newModel(query), tea.WithAltScreen()).Run(); err != nil {
		die(err.Error())
	}
}

func die(msg string) {
	fmt.Fprintln(os.Stderr, "lindeck:", msg)
	os.Exit(1)
}
