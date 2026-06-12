package postgres

import "strings"

// likeEscaper escapes the LIKE/ILIKE pattern metacharacters (% and _)
// and the default escape character (backslash) so a user-supplied
// search term matches literally when embedded in a pattern such as
// "%" + EscapeLike(term) + "%". Postgres' default ESCAPE character is
// backslash, so call sites don't need an explicit ESCAPE clause.
var likeEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

// EscapeLike returns s with all LIKE/ILIKE metacharacters escaped.
// Use for every operator-typed search term that reaches an ILIKE
// pattern — without it, a term like "100%" matches everything and
// "_" matches any single character.
func EscapeLike(s string) string {
	return likeEscaper.Replace(s)
}
