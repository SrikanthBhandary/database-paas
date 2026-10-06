package database

import "errors"

var (
	ErrNotFound      = errors.New("database not found")
	ErrAlreadyExists = errors.New("database already exists")
	ErrInvalidInput  = errors.New("invalid input")
)
