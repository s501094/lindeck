package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Source is one package manager lindeck can drive: apt, Homebrew, Flatpak,
// and so on, or one the user defined in config.json. Each capability is a
// function so sources only implement what their tool supports.
type Source struct {
	ID, Name   string
	bins       []string // command names or absolute paths to look for
	bin        string   // resolved path, set by detect
	detected   bool
	Root       bool   // installing and removing packages needs sudo
	Catalog    bool   // the whole catalog can be listed and filtered locally
	Sections   bool   // the catalog has sections to browse (apt)
	SearchHint string // shown on the Browse tab for search-only sources
	Custom     bool   // defined by the user in config.json

	catalog    func(s *Source) ([]*Pkg, error)
	search     func(s *Source, q string) ([]*Pkg, error)
	installed  func(s *Source) ([]Inst, error)
	outdated   func(s *Source) (map[string]string, error) // name → newer version
	info       func(s *Source, p *Pkg) []string
	install    func(s *Source, p *Pkg) []string
	remove     func(s *Source, p *Pkg) []string
	upgrade    func(s *Source, p *Pkg) []string
	upgradeAll func(s *Source) []string

	RepoKind string // what this source calls a repository: "repository", "tap", "remote"
	RepoHint string // example shown when adding one
	repos    func(s *Source) ([]Repo, error)
	addRepo  func(s *Source, spec string) (*job, error)
	delRepo  func(s *Source, r Repo) *job

	SetupHint string      // what installing this package manager will do
	setup     func() *job // installs the package manager itself
}

// Repo is a repository inside a source: an apt list file, a brew tap, a
// Flatpak remote.
type Repo struct {
	Name, Detail string
	Path         string // file backing it, for apt
	System       bool   // a Flatpak remote in the system installation
	Protected    bool   // the distro's own sources, never removed from lindeck
}

// Bin returns the full path of the source's command, or "" if it isn't
// installed. The lookup is done once: on WSL, PATH usually includes dozens of
// Windows directories, and searching them for a missing command takes
// ~200ms. allSources builds fresh Sources when a manager gets installed.
func (s *Source) Bin() string {
	if !s.detected {
		s.detect()
	}
	return s.bin
}

func (s *Source) detect() {
	s.detected, s.bin = true, ""
	for _, b := range s.bins {
		if filepath.IsAbs(b) {
			if st, err := os.Stat(b); err == nil && !st.IsDir() {
				s.bin = b
				return
			}
			continue
		}
		if p, err := exec.LookPath(b); err == nil {
			s.bin = p
			return
		}
	}
}

func (s *Source) Available() bool { return s.Bin() != "" }

func (s *Source) pkgJob(verb string, args []string, p *Pkg) *job {
	if args == nil {
		return nil
	}
	return &job{label: verb + " " + p.Name, args: args, root: s.Root, reloadSource: s.ID}
}

// job is a command run in the background with its output streamed into the
// log pane. Jobs can chain: next runs after this one succeeds.
type job struct {
	label   string
	args    []string
	root    bool     // run through sudo
	preauth bool     // runs as you, but calls sudo itself: get the password first
	env     []string // extra environment variables
	next    *job

	reloadSource string // reload this source's catalog and installed list after
	reloadRepos  string // reload this source's repositories after
	redetect     bool   // a package manager was installed: re-check sources

	lines chan string
	done  chan error
}

// needsPassword reports whether any job in the chain needs sudo.
func (j *job) needsPassword() bool {
	for ; j != nil; j = j.next {
		if j.root || j.preauth {
			return true
		}
	}
	return false
}

// chain links jobs in order, skipping nils, and returns the first.
func chain(jobs ...*job) *job {
	var first, last *job
	for _, j := range jobs {
		if j == nil {
			continue
		}
		if first == nil {
			first = j
		} else {
			last.next = j
		}
		last = j
		for last.next != nil {
			last = last.next
		}
	}
	return first
}

// runStream runs a job and sends each output line to lines, closing it when
// the command exits; the final error goes on done. A root job with an empty
// password relies on sudo's cached credentials.
func runStream(j *job, password string, lines chan<- string, done chan<- error) {
	var cmd *exec.Cmd
	if j.root {
		sudoArgs := []string{"-S", "-p", "", "--"}
		if password == "" {
			sudoArgs = []string{"-n", "--"}
		}
		cmd = exec.Command("sudo", append(sudoArgs, j.args...)...)
		if password != "" {
			cmd.Stdin = strings.NewReader(password + "\n")
		}
	} else {
		cmd = exec.Command(j.args[0], j.args[1:]...)
	}
	cmd.Env = append(os.Environ(), "DEBIAN_FRONTEND=noninteractive", "NONINTERACTIVE=1",
		"HOMEBREW_NO_ENV_HINTS=1", "NPM_CONFIG_FUND=false")
	cmd.Env = append(cmd.Env, j.env...)
	pr, pw := io.Pipe()
	cmd.Stdout, cmd.Stderr = pw, pw

	if err := cmd.Start(); err != nil {
		close(lines)
		done <- err
		return
	}
	go func() {
		sc := bufio.NewScanner(pr)
		sc.Buffer(make([]byte, 64<<10), 1<<20)
		for sc.Scan() {
			lines <- sc.Text()
		}
		close(lines)
	}()
	err := cmd.Wait()
	pw.Close()
	done <- err
}

// output runs a command and returns its stdout. Commands that talk to the
// network get a generous timeout so a dead connection can't hang lindeck.
func output(bin string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = append(os.Environ(), "HOMEBREW_NO_AUTO_UPDATE=1", "HOMEBREW_NO_ENV_HINTS=1")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil && len(out) == 0 {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return nil, errors.New(firstLine(msg))
		}
		return nil, err
	}
	return out, nil // some tools exit non-zero with useful output (npm outdated)
}

// outLines runs a command and returns its non-empty stdout lines.
func outLines(bin string, args ...string) ([]string, error) {
	out, err := output(bin, args...)
	if err != nil {
		return nil, err
	}
	return splitLines(out), nil
}

func splitLines(out []byte) []string {
	var res []string
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	for sc.Scan() {
		if l := strings.TrimRight(sc.Text(), " \t\r"); strings.TrimSpace(l) != "" {
			res = append(res, l)
		}
	}
	return res
}

func firstLine(s string) string {
	l, _, _ := strings.Cut(s, "\n")
	return l
}

// allSources returns every source lindeck knows: the built-in ones, then
// any the user defined in config.json.
func allSources(cfg Config) []*Source {
	srcs := builtinSources()
	for _, c := range cfg.Sources {
		srcs = append(srcs, c.source())
	}
	for _, s := range srcs {
		s.detect() // up front, so background commands only ever read it
	}
	return srcs
}
