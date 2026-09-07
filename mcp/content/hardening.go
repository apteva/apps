package main

import (
	"database/sql"
	"errors"
	"net/http"
)

// Shared by database helpers that must run inside a transaction.
type contentQuerier interface {
	Query(string, ...any) (*sql.Rows, error)
	QueryRow(string, ...any) *sql.Row
}

var errEditConflict = errors.New("content changed since it was loaded; reload before saving")

func httpContentError(w http.ResponseWriter, err error) {
	code := http.StatusBadRequest
	if errors.Is(err, errEditConflict) {
		code = http.StatusConflict
	}
	httpErr(w, code, err.Error())
}
func firstPatchedLocale(patch *string, prior string) string {
	if patch != nil {
		return *patch
	}
	return prior
}
