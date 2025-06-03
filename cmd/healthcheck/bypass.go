package main

import (
	"log"
)

func isFullClusterCrash() bool {
	fullClusterCrash, err := fileExists("/var/lib/mysql/full-cluster-crash")
	if err != nil {
		log.Printf("check /var/lib/mysql/full-cluster-crash: %s", err)
		return false
	}

	return fullClusterCrash
}

func isManualRecovery() bool {
	manualRecovery, err := fileExists("/var/lib/mysql/sleep-forever")
	if err != nil {
		log.Printf("check /var/lib/mysql/sleep-forever: %s", err)
		return false
	}

	return manualRecovery
}
