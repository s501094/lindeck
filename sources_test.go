package main

import "testing"

func TestCleanLine(t *testing.T) {
	for in, want := range map[string]string{
		"(Reading database ... 5%\r(Reading database ... 100%\r": "(Reading database ... 100%",
		"\x1b[1;32m==>\x1b[0m Pouring wget":                      "==> Pouring wget",
		"a\tb":                                                   "a    b",
		"plain line":                                             "plain line",
		"50%\r100%":                                              "100%",
		"\x1b]0;title\x07x":                                      "x",
	} {
		if got := cleanLine(in); got != want {
			t.Errorf("cleanLine(%q) = %q, want %q", in, got, want)
		}
	}
}
