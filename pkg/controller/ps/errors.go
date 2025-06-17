package ps

import "github.com/pkg/errors"

var (
	ErrNoReadyPods = errors.New("no ready pods")
)
