package main

import (
	"bufio"
	"bytes"
	"os/exec"
	"sort"
	"strings"
)

// Inst is one package dpkg knows is installed (or broken mid-install).
type Inst struct {
	Name, Version, Summary string
	Manual                 bool
	State                  string // "" when healthy, otherwise a dpkg state like "half-configured"
}

var dpkgStates = map[byte]string{
	'U': "unpacked, not configured",
	'F': "half-configured",
	'H': "half-installed",
	'W': "awaiting triggers",
	't': "triggers pending",
}

func lines(name string, args ...string) []string {
	out, _ := exec.Command(name, args...).Output()
	var res []string
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		if l := strings.TrimSpace(sc.Text()); l != "" {
			res = append(res, l)
		}
	}
	return res
}

// loadInstalled lists installed packages, flagging which were installed by
// hand (apt-mark showmanual) versus pulled in as dependencies.
func loadInstalled() []Inst {
	manual := map[string]bool{}
	for _, n := range lines("apt-mark", "showmanual") {
		manual[n] = true
	}
	var res []Inst
	for _, l := range lines("dpkg-query", "-W", "-f=${db:Status-Abbrev}\t${Package}\t${Version}\t${binary:Summary}\n") {
		f := strings.SplitN(l, "\t", 4)
		if len(f) < 4 || len(f[0]) < 2 {
			continue
		}
		st := f[0][1]
		if st == 'n' || st == 'c' { // not installed / only config files left
			continue
		}
		in := Inst{Name: f[1], Version: f[2], Summary: f[3], Manual: manual[f[1]]}
		if st != 'i' {
			in.State = dpkgStates[st]
			if in.State == "" {
				in.State = "dpkg state " + string(st)
			}
		}
		res = append(res, in)
	}
	sort.Slice(res, func(i, j int) bool { return res[i].Name < res[j].Name })
	return res
}

// Check is one health-scan result.
type Check struct {
	Name   string
	Status string // "ok" | "warn" | "err"
	Notes  []string
}

// runScan checks installed packages for broken dpkg states, pending upgrades,
// holds, and orphaned dependencies. Manually installed packages with no
// issues are listed as ok.
func runScan(inst []Inst) []Check {
	checks := map[string]*Check{}
	get := func(name string) *Check {
		if c := checks[name]; c != nil {
			return c
		}
		c := &Check{Name: name, Status: "ok"}
		checks[name] = c
		return c
	}
	flag := func(name, status, note string) {
		c := get(name)
		if status == "err" || c.Status == "ok" {
			c.Status = status
		}
		c.Notes = append(c.Notes, note)
	}

	for _, in := range inst {
		if in.State != "" {
			flag(in.Name, "err", "broken: "+in.State+" — try `sudo dpkg --configure -a`")
		} else if in.Manual {
			get(in.Name)
		}
	}
	// "name/noble-updates 1.2 amd64 [upgradable from: 1.1]"
	for _, l := range lines("apt", "list", "--upgradable") {
		name, rest, ok := strings.Cut(l, "/")
		if !ok {
			continue
		}
		f := strings.Fields(rest)
		note := "upgradable"
		if len(f) >= 2 {
			note = "upgradable → " + f[1]
			if i := strings.Index(rest, "from: "); i >= 0 {
				note = "upgradable " + strings.TrimSuffix(rest[i+6:], "]") + " → " + f[1]
			}
		}
		flag(name, "warn", note)
	}
	for _, n := range lines("apt-mark", "showhold") {
		flag(n, "warn", "held back (apt-mark hold)")
	}
	// "Remv name [1.2]"
	for _, l := range lines("apt-get", "-s", "autoremove") {
		if f := strings.Fields(l); len(f) >= 2 && f[0] == "Remv" {
			flag(f[1], "warn", "auto-installed and no longer needed — A to autoremove")
		}
	}

	order := map[string]int{"err": 0, "warn": 1, "ok": 2}
	res := make([]Check, 0, len(checks))
	for _, c := range checks {
		res = append(res, *c)
	}
	sort.Slice(res, func(i, j int) bool {
		if order[res[i].Status] != order[res[j].Status] {
			return order[res[i].Status] < order[res[j].Status]
		}
		return res[i].Name < res[j].Name
	})
	return res
}

// pkgDetails returns `apt-cache show` for the candidate version as display lines.
func pkgDetails(name string) []string {
	out, err := exec.Command("apt-cache", "show", "--no-all-versions", "--", name).Output()
	if err != nil {
		return []string{"no details available for " + name}
	}
	keep := map[string]bool{"Version": true, "Section": true, "Installed-Size": true,
		"Maintainer": true, "Homepage": true, "Depends": true, "Origin": true}
	var res, desc []string
	inDesc := false
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		l := sc.Text()
		if l == "" {
			break
		}
		if l[0] == ' ' {
			if inDesc {
				if strings.TrimSpace(l) == "." {
					l = ""
				}
				desc = append(desc, strings.TrimPrefix(l, " "))
			}
			continue
		}
		key, val, _ := strings.Cut(l, ": ")
		inDesc = key == "Description" || key == "Description-en"
		switch {
		case inDesc:
			desc = append(desc, val, "")
		case key == "Installed-Size":
			res = append(res, padKey(key)+val+" kB")
		case keep[key]:
			res = append(res, padKey(key)+val)
		}
	}
	return append(append(res, ""), desc...)
}

func padKey(k string) string {
	k = strings.ToLower(k) + ":"
	for len(k) < 16 {
		k += " "
	}
	return k
}

// openURL opens a link in the desktop browser, preferring wslview under WSL.
func openURL(url string) error {
	for _, opener := range []string{"wslview", "xdg-open"} {
		if p, err := exec.LookPath(opener); err == nil {
			return exec.Command(p, url).Start()
		}
	}
	return exec.ErrNotFound
}
