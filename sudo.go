package main

import (
	"errors"
	"os"
	"os/exec"
	"strings"
)

// sudoCached reports whether sudo will run without asking for a password.
func sudoCached() bool {
	return exec.Command("sudo", "-n", "true").Run() == nil
}

// sudoValidate checks a password by refreshing sudo's credential cache.
func sudoValidate(password string) error {
	cmd := exec.Command("sudo", "-S", "-k", "-p", "", "-v")
	cmd.Stdin = strings.NewReader(password + "\n")
	if err := cmd.Run(); err != nil {
		return errors.New("incorrect password")
	}
	return nil
}

// sudoInteractive runs args as root attached to the terminal, letting sudo
// prompt for the password itself. Used by the plain CLI subcommands.
func sudoInteractive(args []string) error {
	cmd := exec.Command("sudo", append([]string{"--"}, args...)...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}
