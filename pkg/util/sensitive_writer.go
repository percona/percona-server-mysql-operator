package util

import (
	"io"
	"regexp"
)

type SensitiveWriter struct {
	writer io.Writer
	regexp *regexp.Regexp
}

// NewSensitiveWriter creates a new SensitiveWriter that wraps an io.Writer and masks sensitive information.
func NewSensitiveWriter(w io.Writer, r *regexp.Regexp) *SensitiveWriter {
	return &SensitiveWriter{w, r}
}

// Write masks sensitive information in the provided byte slice and writes it to the underlying writer.
func (s *SensitiveWriter) Write(p []byte) (n int, err error) {
	_, err = s.writer.Write(s.regexp.ReplaceAll(p, []byte(`:*****@`)))
	return len(p), err
}
