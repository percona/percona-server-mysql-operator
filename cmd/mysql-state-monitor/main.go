package main

import (
	"log"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"

	state "github.com/percona/percona-server-mysql-operator/cmd/internal/naming"
	"github.com/percona/percona-server-mysql-operator/pkg/naming"
)

func parseDatum(datum string) state.MySQLState {
	lines := strings.Split(datum, "\n")

	if lines[0] == "READY=1" {
		return state.MySQLReady
	}

	if lines[0] == "STOPPING=1" {
		return state.MySQLDown
	}

	if strings.HasPrefix(lines[0], "STATUS=") {
		status := strings.TrimPrefix(lines[0], "STATUS=")
		switch status {
		case "Server is operational":
			return state.MySQLReady
		case "Server shutdown in progress":
			return state.MySQLDown
		case "Server startup in progress",
			"Data Dictionary upgrade in progress",
			"Data Dictionary upgrade complete",
			"Server upgrade in progress",
			"Server upgrade complete",
			"Server downgrade in progress",
			"Server downgrade complete",
			"Data Dictionary upgrade from MySQL 5.7 in progress",
			"Data Dictionary upgrade from MySQL 5.7 complete",
			"Server shutdown complete": // we treat this as startup because during init, MySQL notifies this even if it's up
			return state.MySQLStartup
		}
	}

	return state.MySQLUnknown
}

func main() {
	log.Println("Starting mysql-state-monitor")

	socketPath, ok := os.LookupEnv(naming.EnvMySQLNotifySocketInternal)
	if !ok {
		log.Fatalln("MYSQL_NOTIFY_SOCKET env variable is required")
	}

	stateFilePath, ok := os.LookupEnv(naming.EnvMySQLStateFile)
	if !ok {
		log.Fatalln("MYSQL_STATE_FILE env variable is required")
	}

	if _, err := os.Stat(socketPath); err == nil {
		if err := os.Remove(socketPath); err != nil {
			log.Fatalf("Failed to remove %s: %s", socketPath, err)
		}
	}

	addr, err := net.ResolveUnixAddr("unixgram", socketPath)
	if err != nil {
		log.Fatalf("Failed to resolve unix addr %s: %s", socketPath, err)
	}

	conn, err := net.ListenUnixgram("unixgram", addr)
	if err != nil {
		log.Fatalf("Failed to listen unixgram %s: %s", socketPath, err)
	}
	defer conn.Close()

	run(conn, stateFilePath)
}

func run(conn *net.UnixConn, stateFilePath string) {
	sigterm := make(chan os.Signal, 1)
	signal.Notify(sigterm, os.Interrupt, syscall.SIGTERM)

	buf := make([]byte, 256)
	for {
		select {
		case sig := <-sigterm:
			log.Printf("Received signal %v. Exiting mysql-state-monitor", sig)
			return
		default:
			n, _, err := conn.ReadFromUnix(buf)
			if err != nil {
				log.Printf("Failed to read from unix socket: %s", err)
				continue
			}
			datum := string(buf[:n])
			mysqlState := parseDatum(datum)

			log.Printf("MySQLState: %s\nReceived: %s", mysqlState, datum)

			if err := os.WriteFile(stateFilePath, []byte(mysqlState), 0666); err != nil {
				log.Printf("Failed to write to state file: %s", err)
			}
		}
	}
}
