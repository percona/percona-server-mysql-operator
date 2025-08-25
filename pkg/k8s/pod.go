package k8s

import (
	"strconv"
	"strings"

	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
)

func GetIdxFromPod(pod *corev1.Pod) (int, error) {
	s := strings.Split(pod.Name, "-")
	if len(s) < 1 {
		return -1, errors.New("unexpected pod name")
	}

	return strconv.Atoi(s[len(s)-1])
}
