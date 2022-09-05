package main

import (
	"context"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"
	"strings"

	apiv1alpha1 "github.com/percona/percona-server-mysql-operator/api/v1alpha1"
	"github.com/percona/percona-server-mysql-operator/pkg/mysql"
	"github.com/percona/percona-server-mysql-operator/pkg/replicator"
	"github.com/pkg/errors"
	uzap "go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
)

func main() {
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&zap.Options{
		Encoder: zapcore.NewConsoleEncoder(uzap.NewDevelopmentEncoderConfig()),
		Level:   zapcore.DebugLevel,
	})))

	clusterHint := os.Getenv("CLUSTER_HINT")

	operatorPass, err := getSecret(apiv1alpha1.UserOperator)
	if err != nil {
		log.Fatal(err)
	}

	primary, replicas, err := replicator.GetTopology(context.TODO(), clusterHint, apiv1alpha1.UserOperator, operatorPass)
	if err != nil {
		log.Fatal(err)
	}

	log.Printf("primary: %s", primary)
	for i, replica := range replicas {
		log.Printf("replica %d: %s", i, replica)
	}
}

func getSecret(username apiv1alpha1.SystemUser) (string, error) {
	path := filepath.Join(mysql.CredsMountPath, string(username))
	sBytes, err := ioutil.ReadFile(path)
	if err != nil {
		return "", errors.Wrapf(err, "read %s", path)
	}

	return strings.TrimSpace(string(sBytes)), nil
}
