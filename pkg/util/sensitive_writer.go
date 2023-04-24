package util

import (
	"io"
	"regexp"
)

type SensitiveWriter struct {
	writer io.Writer
	regexp *regexp.Regexp
}

func NewSensitiveWriter(w io.Writer, r *regexp.Regexp) *SensitiveWriter {
	return &SensitiveWriter{w, r}
}

func (s *SensitiveWriter) Write(p []byte) (n int, err error) {
	_, err = s.writer.Write(s.regexp.ReplaceAll(p, []byte(`:*****@`)))
	return len(p), err
}
