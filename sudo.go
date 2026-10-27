package main

import (
	"bufio"
	"errors"
	"io"
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

// sudoStream runs args as root and sends each output line to lines, closing it
// when the command exits. An empty password means sudo credentials are cached.
// The final error (nil on success) is sent on done.
func sudoStream(password string, args []string, lines chan<- string, done chan<- error) {
	sudoArgs := []string{"-S", "-p", "", "--"}
	if password == "" {
		sudoArgs = []string{"-n", "--"}
	}
	cmd := exec.Command("sudo", append(sudoArgs, args...)...)
	cmd.Env = append(os.Environ(), "DEBIAN_FRONTEND=noninteractive")
	if password != "" {
		cmd.Stdin = strings.NewReader(password + "\n")
	}
	pr, pw := io.Pipe()
	cmd.Stdout, cmd.Stderr = pw, pw

	if err := cmd.Start(); err != nil {
		close(lines)
		done <- err
		return
	}
	go func() {
		sc := bufio.NewScanner(pr)
		for sc.Scan() {
			lines <- sc.Text()
		}
		close(lines)
	}()
	err := cmd.Wait()
	pw.Close()
	done <- err
}

// sudoInteractive runs args as root attached to the terminal, letting sudo
// prompt for the password itself. Used by the plain CLI subcommands.
func sudoInteractive(args []string) error {
	cmd := exec.Command("sudo", append([]string{"--"}, args...)...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}
