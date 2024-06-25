package ps

import (
	"context"
	"os"

	"github.com/go-logr/logr"
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

	updated := updateImages(cr, version, log, true)
	if !updated {
		return nil
	}

	nn := client.ObjectKeyFromObject(cr)
	err = k8sretry.RetryOnConflict(k8sretry.DefaultRetry, func() error {
		cr, err := k8s.GetCRWithDefaults(ctx, r.Client, nn, r.ServerVersion)
		if err != nil {
			return errors.Wrap(err, "get cr with defaults")
		}

		_ = updateImages(cr, version, log, false)

		return r.Client.Update(ctx, cr)
	})
	if err != nil {
		log.Info("failed to update CR, using the default version")
		return errors.Wrap(err, "failed to update CR")
	}

	return ErrShouldReconcile
}

func updateImages(cr *apiv1alpha1.PerconaServerMySQL, version vs.DepVersion, log logr.Logger, updateStatus bool) bool {
	type ptrStr struct {
		currentImage   *string
		currentVersion *string

		newImage   string
		newVersion string
	}

	m := map[string]ptrStr{
		"MySQL": {
			&cr.Spec.MySQL.Image,
			&cr.Status.MySQL.Version,
			version.PSImage,
			version.PSVersion,
		},
		"backup": {
			&cr.Spec.Backup.Image,
			&cr.Status.BackupVersion,
			version.BackupImage,
			version.BackupVersion,
		},
		"orchestrator": {
			&cr.Spec.Orchestrator.Image,
			&cr.Status.Orchestrator.Version,
			version.OrchestratorImage,
			version.OrchestratorVersion,
		},
		"MySQL Router": {
			&cr.Spec.Proxy.Router.Image,
			&cr.Status.Router.Version,
			version.RouterImage,
			version.RouterVersion,
		},
		"PMM": {
			&cr.Spec.Proxy.Router.Image,
			&cr.Status.Router.Version,
			version.RouterImage,
			version.RouterVersion,
		},
		"HAProxy": {
			&cr.Spec.Proxy.HAProxy.Image,
			&cr.Status.HAProxy.Version,
			version.HAProxyImage,
			version.HAProxyVersion,
		},
		"Percona Toolkit": {
			&cr.Spec.Toolkit.Image,
			&cr.Status.ToolkitVersion,
			version.ToolkitImage,
			version.ToolkitVersion,
		},
	}

	updated := false
	for name, s := range m {
		if *s.currentImage == s.newImage {
			continue
		}

		updated = true
		*s.currentImage = s.newImage

		if !updateStatus {
			continue
		}

		if *s.currentVersion == "" {
			log.Info("set " + name + " version to " + s.newVersion)
		} else {
			log.Info("update "+name+" version", "old version", *s.currentVersion, "new version", s.newVersion)
		}
		*s.currentVersion = s.newVersion
	}

	return updated
}
