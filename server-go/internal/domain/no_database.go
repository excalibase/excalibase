package domain

import "errors"

// ErrNoDatabase refuses an operation that needs the project's database on a
// project that has none (EXC-426). Callers answer it with 409: the project
// exists, and adding a database is what makes the operation possible.
var ErrNoDatabase = errors.New("project has no database")
