package main

import "fmt"

type graphqlError struct {
	Code    string
	Message string
}

func (e *graphqlError) Error() string { return e.Message }

func invalid(format string, args ...any) error {
	return &graphqlError{Code: "invalid_request", Message: fmt.Sprintf(format, args...)}
}

func forbidden(message string) error {
	return &graphqlError{Code: "permission_denied", Message: message}
}

func internal(message string) error {
	return &graphqlError{Code: "internal_error", Message: message}
}
