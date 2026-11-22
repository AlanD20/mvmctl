package username

import "regexp"

var guestUsername = regexp.MustCompile(`^[a-z_][a-z0-9_-]*$`)

// Valid reports whether a name uses the supported guest account grammar.
func Valid(name string) bool {
	return guestUsername.MatchString(name)
}
