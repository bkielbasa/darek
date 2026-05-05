package whatsapp

import (
	"database/sql"

	"modernc.org/sqlite"
)

// whatsmeow's sqlstore opens its session DB via sql.Open("sqlite3", ...).
// The conventional driver under that name is github.com/mattn/go-sqlite3,
// which requires CGO. This binary builds with CGO_ENABLED=0 (distroless
// static), so re-register modernc.org/sqlite — pure Go — under "sqlite3"
// so sqlstore.New works without a libc.
func init() {
	sql.Register("sqlite3", &sqlite.Driver{})
}
