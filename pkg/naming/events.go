package naming

const (
	EventStorageClassNotSupportResize = "StorageClassNotSupportResize"
	EventExceededQuota                = "ExceededQuota"

	// EventFailoverWaiting is emitted when a failover starts recovering the
	// transactions stranded on the dead primary, so the wait is visible before
	// the timeout decides anything.
	EventFailoverWaiting = "FailoverWaiting"
	// EventFailoverBlocked is emitted when a failover has spent its whole
	// timeout without recovering the transactions stranded on the dead primary
	// and the configured policy is to leave the cluster without one.
	EventFailoverBlocked = "FailoverBlocked"
	// EventFailoverForced is emitted when the same timeout expires and the
	// configured policy is to promote anyway, giving those transactions up.
	EventFailoverForced = "FailoverForced"
	// EventFailoverFailed is emitted when Orchestrator gives up on a recovery.
	EventFailoverFailed = "FailoverFailed"
	// EventFailoverUnprotected is emitted when a failover runs without
	// backups, which leaves nothing to recover the stranded transactions from
	// and Orchestrator promotes a replica on its own.
	EventFailoverUnprotected = "FailoverUnprotected"
)

const (
	ConditionReasonErrorReconcile = "ErrorReconcile"
)
