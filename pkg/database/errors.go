package database

import (
	"errors"
	"fmt"
)

var (
	ErrNotFound      = errors.New("database not found")
	ErrAlreadyExists = errors.New("database already exists")
	ErrInvalidInput  = errors.New("invalid input")
	ErrInvalidState  = errors.New("operation not allowed in the current state")
)

type ValidationError struct {
	Fields map[string]string
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("%d invalid field(s)", len(e.Fields))
}

func (e *ValidationError) Unwrap() error {
	return ErrInvalidInput
}
