package database

import (
	corev1 "k8s.io/api/core/v1"
)

type Database interface {
	Container() (corev1.Container, error)
}
