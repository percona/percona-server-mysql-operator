package manager

import "errors"

// ClusterSetManagerError implements the error interface
var _ error = &ClusterSetManagerError{}

type ClusterSetManagerError struct {
	reason  string
	message string
}

func (e *ClusterSetManagerError) Error() string {
	return e.reason + ": " + e.message
}

func (e *ClusterSetManagerError) Reason() string {
	return e.reason
}

func (e *ClusterSetManagerError) Message() string {
	return e.message
}

const (
	reasonPrimaryUnreachable = "PrimaryUnreachable"
	reasonAccessDenied       = "AccessDenied"
	reasonGenericError       = "ClusterSetManagerError"
)

func newClusterSetManagerError(reason, message string) *ClusterSetManagerError {
	return &ClusterSetManagerError{
		reason:  reason,
		message: message,
	}
}

// NewUnreachableError returns an error indicating that no endpoint of the
// primary cluster could be reached.
func NewUnreachableError() *ClusterSetManagerError {
	return newClusterSetManagerError(
		reasonPrimaryUnreachable,
		"No reachable endpoint for the primary cluster",
	)
}

// NewAccessDeniedError returns an error indicating that the clusterset
// credentials were rejected by the primary cluster.
func NewAccessDeniedError() *ClusterSetManagerError {
	return newClusterSetManagerError(
		reasonAccessDenied,
		"Invalid credentials for the primary cluster",
	)
}

// NewGenericError returns an error for any other cluster set manager failure.
func NewGenericError(message string) *ClusterSetManagerError {
	return newClusterSetManagerError(
		reasonGenericError,
		message,
	)
}

func AsManagerError(err error) *ClusterSetManagerError {
	target := &ClusterSetManagerError{}
	if errors.As(err, &target) {
		return target
	}
	return nil
}

func IsUnreachableError(err error) bool {
	target := AsManagerError(err)
	return target != nil && target.Reason() == reasonPrimaryUnreachable
}
