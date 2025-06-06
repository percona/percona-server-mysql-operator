package naming

import "github.com/percona/percona-server-mysql-operator/pkg/version"

// Kubernetes documentation on label naming conventions and usage:
// https://kubernetes.io/docs/concepts/overview/working-with-objects/common-labels/

const (
	kubernetesPrefix = "app.kubernetes.io/"

	// The name of the application.
	// This label is used to group all resources associated with the same application, regardless of their role or environment.
	// It should reflect the base application name, not the resource type or instance.
	// Example: "mysql", "haproxy", "orchestrator".
	LabelName = kubernetesPrefix + "name"

	// A unique name identifying the instance of an application
	// This should be set to the ".metadata.name" of the corresponding
	// PerconaServerMySQL, PerconaServerMySQLBackup, or PerconaServerMySQLRestore object.
	LabelInstance = kubernetesPrefix + "instance"

	// The tool being used to manage the operation of an application.
	// This should be set to the operator's official name only if it owns the resource.
	// For example, for cert-manager Certificates, set it to "cert-manager".
	LabelManagedBy = kubernetesPrefix + "managed-by"

	// The name of the higher-level application this resource is a part of.
	// Set this to one of "percona-server", "percona-server-backup", or "percona-server-restore" of the corresponding object.
	// TODO: Would it be better to use "ps", "ps-backup", and "ps-restore" instead?
	LabelPartOf = kubernetesPrefix + "part-of"

	// The role or function of the component within the application architecture.
	// Should match one of the predefined values from the `naming` package.
	LabelComponent = kubernetesPrefix + "component"

	// Operator version. Should use `version.Version()`
	LabelOperatorVersion = kubernetesPrefix + "version"
)

func baseLabels() map[string]string {
	return map[string]string{
		LabelOperatorVersion: "v" + version.Version(),
		LabelManagedBy:       "percona-server-mysql-operator",
	}
}

func Labels(name, instance, partOf, component string) map[string]string {
	l := baseLabels()
	if name != "" {
		l[LabelName] = name
	}
	if instance != "" {
		l[LabelInstance] = instance
	}
	if partOf != "" {
		l[LabelPartOf] = partOf
	}
	if component != "" {
		l[LabelComponent] = component
	}
	return l
}

const (
	ComponentProxy        = "proxy"
	ComponentDatabase     = "database"
	ComponentOrchestrator = "orchestrator"
	ComponentTLS          = "tls"
	ComponentBackup       = "backup"
	ComponentRestore      = "restore"
	ComponentPITR         = "pitr"
)
