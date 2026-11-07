package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Tracker text (titles, descriptions, comments) comes from other people. Before
// tk prints it to a terminal, control characters are removed: an issue title
// with escape sequences could otherwise retitle the terminal, hide text or, in
// some terminals, do worse. Newlines and tabs stay.
func cleanText(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\t':
			return r
		case r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f): // C0, DEL, C1 (incl. 8-bit CSI)
			return -1
		case r == '‪' || r == '‫' || r == '‬' || r == '‭' || r == '‮' ||
			r == '⁦' || r == '⁧' || r == '⁨' || r == '⁩': // bidi overrides ("Trojan Source")
			return -1
		}
		return r
	}, s)
}

var fileNameUnsafe = regexp.MustCompile(`[^A-Za-z0-9#_-]+`)

// safeFileName turns an issue key or id from a tracker (or the command line)
// into a file name that can't climb out of its directory.
func safeFileName(s string) string {
	s = fileNameUnsafe.ReplaceAllString(s, "_")
	if s == "" || strings.Trim(s, "_") == "" {
		s = "_empty"
	}
	if len(s) > 120 {
		s = s[:120]
	}
	return s
}

// shellQuote quotes a path for the POSIX shell that runs hook commands, unless
// it is made only of characters that need no quoting.
func shellQuote(s string) string {
	if regexp.MustCompile(`^[A-Za-z0-9_./-]+$`).MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// tildePath shows a path under the home folder as ~/..., for shorter output.
func tildePath(p string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return p
	}
	home = filepath.Clean(home)
	for _, h := range []string{home, realPath(home)} {
		if p == h {
			return "~"
		}
		if rel, ok := strings.CutPrefix(p, h+string(filepath.Separator)); ok {
			return "~" + string(filepath.Separator) + rel
		}
	}
	return p
}
