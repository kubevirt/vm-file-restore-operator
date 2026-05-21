package controller

import "fmt"

// TransientError represents a temporary error that should be retried.
// Used for conditions like PVC provisioning or resource binding that
// resolve over time.
type TransientError struct {
	msg string
}

// NewTransientError creates a new transient error.
func NewTransientError(msg string) error {
	return &TransientError{msg: msg}
}

// Error implements the error interface.
func (e *TransientError) Error() string {
	return e.msg
}

// IsTransient checks if an error is a TransientError.
func IsTransient(err error) bool {
	_, ok := err.(*TransientError)
	return ok
}

// Unwrap allows errors.Is and errors.As to work.
func (e *TransientError) Unwrap() error {
	return nil
}

// Format implements fmt.Formatter for better error messages.
func (e *TransientError) Format(f fmt.State, verb rune) {
	switch verb {
	case 'v':
		if f.Flag('+') {
			fmt.Fprintf(f, "transient error: %s", e.msg)
			return
		}
		fallthrough
	case 's':
		fmt.Fprint(f, e.msg)
	case 'q':
		fmt.Fprintf(f, "%q", e.msg)
	}
}
