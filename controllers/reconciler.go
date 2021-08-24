package controllers

import (
	"context"
	"time"

	"k8s.io/apimachinery/pkg/types"
)

type Reconciler interface {
	Reconcile(context.Context, types.NamespacedName) error
}

const fiveSeconds = 5 * time.Second

var (
	MysqlReconciler        Reconciler
	MysqlBackupReconciler  Reconciler
	MysqlRestoreReconciler Reconciler
)
