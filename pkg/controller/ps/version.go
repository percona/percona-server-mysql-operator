package ps

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"

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

var mysqldVersionOutput = regexp.MustCompile(`Ver (\d+\.\d+\.\d+(?:-\d+)?)`)

func (r *PerconaServerMySQLReconciler) reconcileMySQLVersion(
	ctx context.Context,
	cr *apiv1.PerconaServerMySQL,
) error {
	log := logf.FromContext(ctx)

	configured, err := cr.ConfiguredMySQLVersion()
	if err != nil {
		log.V(1).Info("MySQL version is unknown until a pod is running",
			"image", cr.Spec.MySQL.Image, "reason", err.Error())
	}

	if configured != "" && cr.Status.MySQL.Version != configured {
		if err := r.setMySQLVersion(ctx, cr, configured, ""); err != nil {
			return errors.Wrap(err, "set configured MySQL version")
		}
		log.V(1).Info("MySQL version taken from the custom resource", "version", configured)
	}

	pod, err := mysql.GetReadyPod(ctx, r.Client, cr)
	if err != nil {
		if errors.Is(err, mysql.ErrNoReadyPods) {
			return nil
		}
		return errors.Wrap(err, "get ready mysql pod")
	}

	imageID, err := k8s.GetImageIDFromPod(pod, mysql.AppName)
	if err != nil {
		return errors.Wrapf(err, "get MySQL image id from %s", pod.Name)
	}

	if cr.Status.MySQL.Version != "" && cr.Status.MySQL.ImageID == imageID {
		return nil
	}

	running, err := r.runningMySQLVersion(ctx, pod)
	if err != nil {
		return err
	}

	// The running version confirms what the operator configured; it replaces it
	// only when neither the spec nor the image tag gave us a version to start with.
	version := configured
	if version == "" {
		version = running
	} else if !mysqlVersionsMatch(configured, running) {
		log.Info("configured MySQL version doesn't match the running one",
			"configured", configured, "running", running, "pod", pod.Name)
		r.Recorder.Event(cr, corev1.EventTypeWarning, "MySQLVersionMismatch",
			fmt.Sprintf("configured MySQL version %s doesn't match %s running in %s", configured, running, pod.Name))
	}

	if err := r.setMySQLVersion(ctx, cr, version, imageID); err != nil {
		return errors.Wrap(err, "set MySQL version")
	}

	log.V(1).Info("MySQL Server Version: " + running)
	return nil
}

func (r *PerconaServerMySQLReconciler) setMySQLVersion(
	ctx context.Context,
	cr *apiv1.PerconaServerMySQL,
	version, imageID string,
) error {
	if err := writeStatus(ctx, r.Client, client.ObjectKeyFromObject(cr), func(status *apiv1.PerconaServerMySQLStatus) error {
		status.MySQL.Version = version
		status.MySQL.ImageID = imageID
		return nil
	}); err != nil {
		return errors.Wrap(err, "write status")
	}

	// The configuration reconcilers run later in this same pass and read the
	// version from the in-memory custom resource.
	cr.Status.MySQL.Version = version
	cr.Status.MySQL.ImageID = imageID
	return nil
}

func (r *PerconaServerMySQLReconciler) runningMySQLVersion(ctx context.Context, pod *corev1.Pod) (string, error) {
	var stdoutb, stderrb bytes.Buffer
	if err := r.ClientCmd.Exec(ctx, pod, mysql.AppName, []string{"mysqld", "--version"}, nil, &stdoutb, &stderrb, false); err != nil {
		return "", errors.Wrapf(err, "run mysqld --version (stdout: %s, stderr: %s)", stdoutb.String(), stderrb.String())
	}

	f := mysqldVersionOutput.FindSubmatch(stdoutb.Bytes())
	if len(f) < 2 {
		return "", errors.Errorf(
			"couldn't extract version information from mysqld --version (stdout: %s, stderr: %s)",
			stdoutb.String(), stderrb.String())
	}

	version, err := v.NewVersion(string(f[1]))
	if err != nil {
		return "", errors.Wrap(err, "parse version")
	}

	return version.String(), nil
}

// mysqlVersionsMatch compares only the segments the configured version spells
// out, so that 8.4 configured against 8.4.3 running is not a mismatch.
func mysqlVersionsMatch(configured, running string) bool {
	cv, err := v.NewVersion(configured)
	if err != nil {
		return false
	}

	rv, err := v.NewVersion(running)
	if err != nil {
		return false
	}

	segments := strings.Count(strings.SplitN(configured, "-", 2)[0], ".") + 1
	cs, rs := cv.Segments(), rv.Segments()
	for i := 0; i < segments && i < len(cs) && i < len(rs); i++ {
		if cs[i] != rs[i] {
			return false
		}
	}

	return true
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
