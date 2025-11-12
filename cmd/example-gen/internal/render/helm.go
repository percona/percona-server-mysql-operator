package render

import (
	"io"
	"io/fs"
	"maps"
	"text/template"

	"github.com/Masterminds/sprig"
	"github.com/elastic/crd-ref-docs/types"
	"github.com/pkg/errors"
)

func Helm(wr io.Writer, t *types.Type, f fs.FS) error {
	tplFS, err := fs.Sub(f, "templates")
	if err != nil {
		return errors.Wrap(err, "sub")
	}

	tmpl, err := template.New("").Funcs(funcMap()).ParseFS(tplFS, "*.tpl")
	if err != nil {
		return errors.Wrap(err, "parse fs")
	}

	if err := tmpl.ExecuteTemplate(wr, "main", t); err != nil {
		return errors.Wrap(err, "execute template")
	}
	return nil
}

func funcMap() template.FuncMap {
	fm := template.FuncMap{
		"helmShouldRenderInner": shouldRenderInner,
		"helmIsRequired":        isRequired,
		"helmCustomValue":       customValue,
		"helmDefault":           defaultValue,
	}
	maps.Copy(fm, sprig.TxtFuncMap())
	return fm
}

func shouldRenderInner(t *types.Type) bool {
	return t != nil && t.Package == "github.com/percona/percona-server-mysql-operator/api/v1" && len(t.Members()) > 0
}

func isRequired(t *types.Field) bool {
	if t == nil {
		return false
	}
	_, ok := t.Markers["kubebuilder:validation:Required"]
	return ok
}

func customValue(keyPath string) string {
	switch keyPath {
	case "spec.mysql.image":
		return `"{{ .Values.mysql.image.repository }}:{{ .Values.mysql.image.tag }}"`
	case "spec.proxy.haproxy.image":
		return `"{{ .Values.proxy.haproxy.image.repository }}:{{ .Values.proxy.haproxy.image.tag }}"`
	case "spec.proxy.router.image":
		return `"{{ .Values.proxy.router.image.repository }}:{{ .Values.proxy.router.image.tag }}"`
	case "spec.orchestrator.image":
		return `"{{ .Values.orchestrator.image.repository }}:{{ .Values.orchestrator.image.tag }}"`
	case "spec.toolkit.image":
		return `"{{ .Values.toolkit.image.repository }}:{{ .Values.toolkit.image.tag }}"`
	case "spec.pmm.image":
		return `"{{ .Values.pmm.image.repository }}:{{ .Values.pmm.image.tag }}"`
	case "spec.crVersion":
		return `{{ .Chart.AppVersion }}`
	case "apiVersion":
		return `ps.percona.com/v1alpha1`
	case "kind":
		return `PerconaServerMySQL`
	case "metadata":
		return `
  annotations:
    kubectl.kubernetes.io/last-applied-configuration: |
      {"apiVersion":"ps.percona.com/v1alpha1","kind":"PerconaServerMySQL"}
  name: {{ include "ps-database.fullname" . }}
  namespace: {{ .Release.Namespace }}
  labels:
{{ include "ps-database.labels" . | indent 4 }}
  finalizers:
{{ .Values.finalizers | toYaml | indent 4 }}`
	default:
		return ""
	}
}

func defaultValue(keyPath string) string {
	switch keyPath {
	case "spec.secretsName":
		return `{{ include "ps-database.fullname" . }}-secrets`
	case "spec.sslSecretName":
		return `{{ include "ps-database.fullname" . }}-ssl`
	case "spec.proxy.haproxy.initContainer.image", "spec.backup.initContainer.image", "spec.initContainer.image", "spec.mysql.initContainer.image":
		return `{{ include "ps-database.operator-image" . }}`
	default:
		return ""
	}
}
