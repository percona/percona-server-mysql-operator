package ps

import (
	"context"
	"os"

	"github.com/pkg/errors"
	k8sretry "k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	apiv1alpha1 "github.com/percona/percona-server-mysql-operator/api/v1alpha1"
	"github.com/percona/percona-server-mysql-operator/pkg/k8s"
	vs "github.com/percona/percona-server-mysql-operator/pkg/version/service"
)

func telemetryEnabled() bool {
	value, ok := os.LookupEnv("DISABLE_TELEMETRY")
	if ok {
		return value != "true"
	}
	return true
}

func versionUpgradeEnabled(cr *apiv1alpha1.PerconaServerMySQL) bool {
	return cr.Spec.UpgradeOptions.Apply != "" &&
		cr.Spec.UpgradeOptions.Apply != apiv1alpha1.UpgradeStrategyDisabled &&
		cr.Spec.UpgradeOptions.Apply != apiv1alpha1.UpgradeStrategyNever
}

func (r *PerconaServerMySQLReconciler) reconcileVersions(ctx context.Context, cr *apiv1alpha1.PerconaServerMySQL) error {
	if !(versionUpgradeEnabled(cr) || telemetryEnabled()) {
		return nil
	}

	log := logf.FromContext(ctx).WithName("reconcileVersions")

	if telemetryEnabled() && (!versionUpgradeEnabled(cr) || cr.Spec.UpgradeOptions.VersionServiceEndpoint != vs.GetDefaultVersionServiceEndpoint()) {
		_, err := vs.GetVersion(ctx, cr, vs.GetDefaultVersionServiceEndpoint(), r.ServerVersion)
		if err != nil {
			log.Error(err, "failed to send telemetry to "+vs.GetDefaultVersionServiceEndpoint())
		}
	}

	if !versionUpgradeEnabled(cr) {
		return nil
	}

	version, err := vs.GetVersion(ctx, cr, cr.Spec.UpgradeOptions.VersionServiceEndpoint, r.ServerVersion)
	if err != nil {
		log.Info("failed to get versions, using the default ones")
		return errors.Wrap(err, "failed to get versions")
	}

	printedMsg := false
	shouldUpdate := false
	nn := client.ObjectKeyFromObject(cr)
	err = k8sretry.RetryOnConflict(k8sretry.DefaultRetry, func() error {
		cr, err := k8s.GetCRWithDefaults(ctx, r.Client, nn, r.ServerVersion)
		if err != nil {
			return errors.Wrap(err, "get cr with defaults")
		}
		logMsg := func(msg string, keysAndValues ...any) {
			if printedMsg {
				return
			}
			shouldUpdate = true
			log.Info(msg, keysAndValues...)
		}

		if cr.Spec.MySQL.Image != version.PSImage {
			if cr.Status.MySQL.Version == "" {
				logMsg("set MySQL version to " + version.PSVersion)
			} else {
				logMsg("update MySQL version", "old version", cr.Status.MySQL.Version, "new version", version.PSVersion)
			}
			cr.Spec.MySQL.Image = version.PSImage
		}
		if cr.Spec.Backup.Image != version.BackupImage {
			if cr.Status.BackupVersion == "" {
				logMsg("set backup version to " + version.BackupVersion)
			} else {
				logMsg("update backup version", "old version", cr.Status.BackupVersion, "new version", version.BackupVersion)
			}
			cr.Spec.Backup.Image = version.BackupImage
		}
		if cr.Spec.Orchestrator.Image != version.OrchestratorImage {
			if cr.Status.Orchestrator.Version == "" {
				logMsg("set orchestrator version to " + version.OrchestratorVersion)
			} else {
				logMsg("update orchestrator version", "old version", cr.Status.Orchestrator.Version, "new version", version.OrchestratorVersion)
			}
			cr.Spec.Orchestrator.Image = version.OrchestratorImage
		}
		if cr.Spec.Proxy.Router.Image != version.RouterImage {
			if cr.Status.Router.Version == "" {
				logMsg("set MySQL router version to " + version.RouterVersion)
			} else {
				logMsg("update MySQL router version", "old version", cr.Status.Router.Version, "new version", version.RouterVersion)
			}
			cr.Spec.Proxy.Router.Image = version.RouterImage
		}
		if cr.Spec.PMM.Image != version.PMMImage {
			if cr.Status.PMMVersion == "" {
				logMsg("set PMM version to " + version.PMMVersion)
			} else {
				logMsg("update PMM version", "old version", cr.Status.PMMVersion, "new version", version.PMMVersion)
			}
			cr.Spec.PMM.Image = version.PMMImage
		}
		if cr.Spec.Proxy.HAProxy.Image != version.HAProxyImage {
			if cr.Status.HAProxy.Version == "" {
				logMsg("set HAProxy version to " + version.HAProxyVersion)
			} else {
				logMsg("update HAProxy version", "old version", cr.Status.HAProxy.Version, "new version", version.HAProxyVersion)
			}
			cr.Spec.Proxy.HAProxy.Image = version.HAProxyImage
		}
		if cr.Spec.Toolkit.Image != version.ToolkitImage {
			if cr.Status.ToolkitVersion == "" {
				logMsg("set Percona Toolkit version to " + version.ToolkitVersion)
			} else {
				logMsg("update Percona Toolkit version", "old version", cr.Status.ToolkitVersion, "new version", version.ToolkitVersion)
			}
			cr.Spec.Toolkit.Image = version.ToolkitImage
		}

		printedMsg = true
		if !shouldUpdate {
			return nil
		}

		return r.Client.Update(ctx, cr)
	})
	if err != nil {
		log.Info("failed to update CR, using the default version")
		return errors.Wrap(err, "failed to update CR")
	}

	cr.Status.MySQL.Version = version.PSVersion
	cr.Status.BackupVersion = version.BackupVersion
	cr.Status.Orchestrator.Version = version.OrchestratorVersion
	cr.Status.Router.Version = version.RouterVersion
	cr.Status.PMMVersion = version.PMMVersion
	cr.Status.HAProxy.Version = version.HAProxyVersion
	cr.Status.ToolkitVersion = version.ToolkitVersion

	if shouldUpdate {
		return ErrShouldReconcile
	}
	return nil
}
