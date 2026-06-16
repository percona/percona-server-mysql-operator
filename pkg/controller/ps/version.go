package ps

import (
	"bytes"
	"context"
	"os"
	"regexp"

	v "github.com/hashicorp/go-version"
	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	"github.com/percona/percona-server-mysql-operator/pkg/haproxy"
	"github.com/percona/percona-server-mysql-operator/pkg/k8s"
	"github.com/percona/percona-server-mysql-operator/pkg/mysql"
	vs "github.com/percona/percona-server-mysql-operator/pkg/version/service"
)

func (r *PerconaServerMySQLReconciler) reconcileVersions(ctx context.Context, cr *apiv1.PerconaServerMySQL) error {
	if err := r.reconcileMySQLVersion(ctx, cr); err != nil {
		return errors.Wrap(err, "reconcile mysql version")
	}

	if err := r.reconcileHAProxyVersion(ctx, cr); err != nil {
		return errors.Wrap(err, "reconcile haproxy version")
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

	if err := writeStatus(ctx, r.Client, client.ObjectKeyFromObject(cr), func(status *apiv1.PerconaServerMySQLStatus) error {
		status.MySQL.ImageID = imageId
		status.MySQL.Version = version.String()
		return nil
	}); err != nil {
		return errors.Wrap(err, "write status")
	}
	log.V(1).Info("MySQL Server Version: " + version.String())
	return nil
}

func (r *PerconaServerMySQLReconciler) reconcileHAProxyVersion(
	ctx context.Context,
	cr *apiv1.PerconaServerMySQL,
) error {
	if !cr.HAProxyEnabled() {
		return nil
	}

	pods, err := k8s.PodsByLabels(ctx, r.Client, haproxy.MatchLabels(cr), cr.Namespace)
	if err != nil {
		return errors.Wrap(err, "get haproxy pods")
	}

	var pod *corev1.Pod
	for i := range pods {
		if k8s.IsPodReady(pods[i]) {
			pod = &pods[i]
			break
		}
	}

	if pod == nil {
		return nil
	}

	imageID, err := k8s.GetImageIDFromPod(pod, haproxy.AppName)
	if err != nil {
		return errors.Wrapf(err, "get HAProxy image id from %s", pod.Name)
	}

	if cr.Status.HAProxy.Version != "" && cr.Status.HAProxy.ImageID == imageID {
		return nil
	}

	var stdoutb, stderrb bytes.Buffer
	err = r.ClientCmd.Exec(ctx, pod, haproxy.AppName, []string{"haproxy", "-v"}, nil, &stdoutb, &stderrb, false)
	if err != nil {
		return errors.Wrapf(err, "run haproxy -v (stdout: %s, stderr: %s)", stdoutb.String(), stderrb.String())
	}

	version, err := parseHAProxyVersion(stdoutb.Bytes())
	if err != nil {
		return errors.Wrapf(err, "extract version from haproxy -v output (stdout: %s, stderr: %s)", stdoutb.String(), stderrb.String())
	}

	if err := writeStatus(ctx, r.Client, client.ObjectKeyFromObject(cr), func(status *apiv1.PerconaServerMySQLStatus) error {
		status.HAProxy.Version = version
		status.HAProxy.ImageID = imageID
		return nil
	}); err != nil {
		return errors.Wrap(err, "write status")
	}

	logf.FromContext(ctx).V(1).Info("HAProxy Version: " + version)
	return nil
}

func parseHAProxyVersion(output []byte) (string, error) {
	re, err := regexp.Compile(`(?i)\bversion\s+([^\s]+)`)
	if err != nil {
		return "", err
	}

	f := re.FindSubmatch(output)
	if len(f) < 2 {
		return "", errors.New("couldn't extract version information")
	}

	return string(f[1]), nil
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
			log.V(1).Info("failed to send telemetry to " + vs.GetDefaultVersionServiceEndpoint())
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

	if err := writeStatus(ctx, r.Client, client.ObjectKeyFromObject(cr), func(status *apiv1.PerconaServerMySQLStatus) error {
		status.MySQL.Version = version.PSVersion
		status.BackupVersion = version.BackupVersion
		status.Orchestrator.Version = version.OrchestratorVersion
		status.Router.Version = version.RouterVersion
		status.PMMVersion = version.PMMVersion
		status.HAProxy.Version = version.HAProxyVersion
		status.ToolkitVersion = version.ToolkitVersion
		return nil
	}); err != nil {
		return errors.Wrap(err, "write status")
	}
	return nil
}
