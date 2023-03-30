package ps

import (
	"context"
	"os"

	"github.com/pkg/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	apiv1alpha1 "github.com/percona/percona-server-mysql-operator/api/v1alpha1"
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

	patch := client.MergeFrom(cr.DeepCopy())
	if cr.Spec.MySQL.Image != version.PSImage {
		if cr.Status.MySQL.Version == "" {
			log.Info("set MySQL version to " + version.PSVersion)
		} else {
			log.Info("update MySQL version", "old version", cr.Status.MySQL.Version, "new version", version.PSVersion)
		}
		cr.Spec.MySQL.Image = version.PSImage
	}
	if cr.Spec.Backup.Image != version.BackupImage {
		if cr.Status.BackupVersion == "" {
			log.Info("set backup version to " + version.BackupVersion)
		} else {
			log.Info("update backup version", "old version", cr.Status.BackupVersion, "new version", version.BackupVersion)
		}
		cr.Spec.Backup.Image = version.BackupImage
	}
	if cr.Spec.Orchestrator.Image != version.OrchestratorImage {
		if cr.Status.Orchestrator.Version == "" {
			log.Info("set orchestrator version to " + version.OrchestratorVersion)
		} else {
			log.Info("update orchestrator version", "old version", cr.Status.Orchestrator.Version, "new version", version.OrchestratorVersion)
		}
		cr.Spec.Orchestrator.Image = version.OrchestratorImage
	}
	if cr.Spec.Proxy.Router.Image != version.RouterImage {
		if cr.Status.Router.Version == "" {
			log.Info("set MySQL router version to " + version.RouterVersion)
		} else {
			log.Info("update MySQL router version", "old version", cr.Status.Router.Version, "new version", version.RouterVersion)
		}
		cr.Spec.Proxy.Router.Image = version.RouterImage
	}
	if cr.Spec.PMM.Image != version.PMMImage {
		if cr.Status.PMMVersion == "" {
			log.Info("set PMM version to " + version.PMMVersion)
		} else {
			log.Info("update PMM version", "old version", cr.Status.PMMVersion, "new version", version.PMMVersion)
		}
		cr.Spec.PMM.Image = version.PMMImage
	}
	if cr.Spec.Proxy.HAProxy.Image != version.HAProxyImage {
		if cr.Status.HAProxy.Version == "" {
			log.Info("set HAProxy version to " + version.HAProxyVersion)
		} else {
			log.Info("update HAProxy version", "old version", cr.Status.HAProxy.Version, "new version", version.HAProxyVersion)
		}
		cr.Spec.Proxy.HAProxy.Image = version.HAProxyImage
	}
	if cr.Spec.Toolkit.Image != version.ToolkitImage {
		if cr.Status.ToolkitVersion == "" {
			log.Info("set Percona Toolkit version to " + version.ToolkitVersion)
		} else {
			log.Info("update Percona Toolkit version", "old version", cr.Status.ToolkitVersion, "new version", version.ToolkitVersion)
		}
		cr.Spec.Toolkit.Image = version.ToolkitImage
	}

	err = r.Patch(ctx, cr.DeepCopy(), patch)
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
	return nil
}
