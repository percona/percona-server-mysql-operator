package ps

import (
	"bytes"
	"context"
	"os"
	"regexp"

	v "github.com/hashicorp/go-version"
	"github.com/pkg/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	"github.com/percona/percona-server-mysql-operator/pkg/k8s"
	"github.com/percona/percona-server-mysql-operator/pkg/mysql"
	vs "github.com/percona/percona-server-mysql-operator/pkg/version/service"
)

func (r *PerconaServerMySQLReconciler) reconcileVersions(ctx context.Context, cr *apiv1.PerconaServerMySQL) error {
	if err := r.reconcileMySQLVersion(ctx, cr); err != nil {
		return errors.Wrap(err, "reconcile mysql version")
	}

	if err := r.upgradeVersions(ctx, cr); err != nil {
		return errors.Wrap(err, "upgrade versions")
	}

	return nil
}

func (r *PerconaServerMySQLReconciler) reconcileMySQLVersion(
	ctx context.Context,
	cr *apiv1.PerconaServerMySQL,
) error {
	log := logf.FromContext(ctx)

	// Skip version reconciliation when cluster is paused
	if cr.Spec.Pause {
		log.V(1).Info("Skipping MySQL version reconciliation - cluster is paused", "cluster", cr.Name, "namespace", cr.Namespace)
		return nil
	}

	pod, err := mysql.GetReadyPod(ctx, r.Client, cr)
	if err != nil {
		if errors.Is(err, mysql.ErrNoReadyPods) {
			return nil
		}
		return errors.Wrap(err, "get ready mysql pod")
	}

	imageId, err := k8s.GetImageIDFromPod(pod, mysql.AppName)
	if err != nil {
		return errors.Wrapf(err, "get MySQL image id from %s", pod.Name)
	}

	if len(cr.Status.MySQL.Version) > 0 && cr.Status.MySQL.ImageID == imageId {
		return nil
	}

	re, err := regexp.Compile(`Ver (\d+\.\d+\.\d+(?:-\d+)?)`)
	if err != nil {
		return err
	}

	var stdoutb, stderrb bytes.Buffer

	err = r.ClientCmd.Exec(ctx, pod, mysql.AppName, []string{"mysqld", "--version"}, nil, &stdoutb, &stderrb, false)
	if err != nil {
		return errors.Wrapf(err, "run mysqld --version (stdout: %s, stderr: %s)", stdoutb.String(), stderrb.String())
	}

	f := re.FindSubmatch(stdoutb.Bytes())
	if len(f) < 1 {
		return errors.Errorf(
			"couldn't extract version information from mysqld --version (stdout: %s, stderr: %s)",
			stdoutb.String(), stderrb.String())
	}

	version, err := v.NewVersion(string(f[1]))
	if err != nil {
		return errors.Wrap(err, "parse version")
	}

	cr.Status.MySQL.ImageID = imageId
	cr.Status.MySQL.Version = version.String()
	log.V(1).Info("MySQL Server Version: " + cr.Status.MySQL.Version)

	return nil
}

func telemetryEnabled() bool {
	value, ok := os.LookupEnv("DISABLE_TELEMETRY")
	if ok {
		return value != "true"
	}
	return true
}

func versionUpgradeEnabled(cr *apiv1.PerconaServerMySQL) bool {
	return cr.Spec.UpgradeOptions.Apply != "" &&
		cr.Spec.UpgradeOptions.Apply != apiv1.UpgradeStrategyDisabled &&
		cr.Spec.UpgradeOptions.Apply != apiv1.UpgradeStrategyNever
}

func (r *PerconaServerMySQLReconciler) upgradeVersions(ctx context.Context, cr *apiv1.PerconaServerMySQL) error {
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
