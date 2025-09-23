package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	"github.com/percona/percona-server-mysql-operator/cmd/sidecar/handler"
	"github.com/percona/percona-server-mysql-operator/pkg/mysql"
)

func startServer() *http.Server {
	log := logf.Log.WithName("startServer")
	mux := http.NewServeMux()

	mux.HandleFunc("/health/", func(w http.ResponseWriter, req *http.Request) {
		fmt.Fprintf(w, "OK")
	})
	mux.Handle("/backup/", handler.NewBackupHandler())
	mux.HandleFunc("/logs/", handler.LogsHandlerFunc)

	srv := &http.Server{Addr: ":" + strconv.Itoa(mysql.SidecarHTTPPort), Handler: mux}

	go func() {
		log.Info("starting http server")
		// always returns error. ErrServerClosed on graceful close
		if err := srv.ListenAndServe(); err != http.ErrServerClosed {
			log.Error(err, "http server failed")
		}
	}()

	return srv
}

func main() {
	log := logf.Log.WithName("sidecar")

	opts := zap.Options{Development: true}
	logf.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	srv := startServer()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	<-stop

	log.Info("received interrupt signal, shutting down http server")

	// TODO: should this timeout use terminationGracePeriodSeconds?
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		log.Error(err, "graceful shutdown failed")
		os.Exit(1)
	}

	os.Exit(0)
}
