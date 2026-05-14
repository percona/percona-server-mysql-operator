package util

import (
	"fmt"
	"io"

	"github.com/pkg/errors"
)

// WrapWithDeepestStack preserves the full error message chain, but limits
// verbose logging to the deepest pkg/errors stack. This avoids the default
// github.com/pkg/errors behavior where %+v prints stack traces from all wraps.
func WrapWithDeepestStack(err error, msg string) error {
	if err == nil {
		return nil
	}

	stackErr := err
	for next := errors.Unwrap(err); next != nil; next = errors.Unwrap(next) {
		if _, ok := next.(stackTracer); ok {
			stackErr = next
		}
	}

	text := err.Error()
	if msg != "" {
		text = msg + ": " + text
	}

	return &deepStackErr{
		msg:   text,
		cause: stackErr,
	}
}

type deepStackErr struct {
	msg   string
	cause error
}

func (e *deepStackErr) Error() string { return e.msg }

func (e *deepStackErr) Cause() error { return e.cause }

func (e *deepStackErr) Unwrap() error { return e.cause }

func (e *deepStackErr) Format(s fmt.State, verb rune) {
	switch verb {
	case 'v':
		if s.Flag('+') {
			_, _ = io.WriteString(s, e.msg)
			if e.cause != nil {
				_, _ = io.WriteString(s, "\n")
				_, _ = fmt.Fprintf(s, "%+v", e.cause)
			}
			return
		}
		fallthrough
	case 's':
		_, _ = io.WriteString(s, e.msg)
	case 'q':
		_, _ = fmt.Fprintf(s, "%q", e.msg)
	}
}

type stackTracer interface {
	StackTrace() errors.StackTrace
}
