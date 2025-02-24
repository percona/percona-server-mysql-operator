# Percona Operator for MySQL

![Percona Kubernetes Operators](kubernetes.svg)

[![License](https://img.shields.io/badge/License-Apache%202.0-blue.svg)](https://opensource.org/licenses/Apache-2.0)
![Docker Pulls](https://img.shields.io/docker/pulls/percona/percona-server-mysql-operator)
![Docker Image Size (latest by date)](https://img.shields.io/docker/image-size/percona/percona-server-mysql-operator)
![GitHub tag (latest by date)](https://img.shields.io/github/v/tag/percona/percona-server-mysql-operator)
![GitHub go.mod Go version](https://img.shields.io/github/go-mod/go-version/percona/percona-server-mysql-operator)
[![Go Report Card](https://goreportcard.com/badge/github.com/percona/percona-server-mysql-operator)](https://goreportcard.com/report/github.com/percona/percona-server-mysql-operator)

[Percona Operator for MySQL based on Percona Server for MySQL](https://docs.percona.com/percona-operator-for-mysql/ps/index.html) automates the creation and management of highly available, enterprise-ready MySQL database clusters on Kubernetes.

[Percona Operator for MySQL based on Percona Server for MySQL](https://www.percona.com/doc/kubernetes-operator-for-mysql/ps/index.html) follows our best practices for deployment and configuration of highly-available, fault-tolerant MySQL instances in a Kubernetes-based environment on-premises or in the cloud. It provides the following capabilities:

* Deploy group replication MySQL clusters with HAProxy or MySQL Router
* Deploy asynchronous replication MySQL clusters with Orchestrator and HAProxy
* Expose clusters with regular Kubernetes Services
* Monitor the cluster with [Percona Monitoring and Management](https://www.percona.com/software/database-tools/percona-monitoring-and-management)
* Customize MySQL configuration
* Manage system user passwords


## Status

**This project is in the tech preview state right now. Don't use it on production.**

As of today, we recommend using [Percona Operator for MySQL based on Percona XtraDB Cluster](https://docs.percona.com/percona-operator-for-mysql/pxc/index.html), which is production-ready and contains everything you need to quickly and consistently deploy and scale MySQL clusters in a Kubernetes-based environment, on-premises or in the cloud.

## Architecture

Percona Operators are based on the [Operator SDK](https://github.com/operator-framework/operator-sdk) and leverage Kubernetes primitives to follow best CNCF practices.

## Documentation

To learn more about the Operator, check the [Percona Operator for MySQL documentation](https://docs.percona.com/percona-operator-for-mysql/ps/index.html).

# Quickstart installation

Ready to try out the Operator? Check the [Quickstart tutorials](https://docs.percona.com/percona-operator-for-mysql/ps/helm.html) for easy-to follow steps. 

Below is one of the ways to deploy the Operator using `kubectl`.

## kubectl

1. Deploy the Operator from `deploy/bundle.yaml`:

```sh
kubectl apply -f https://raw.githubusercontent.com/percona/percona-server-mysql-operator/main/deploy/bundle.yaml
```

2. Deploy the database cluster itself from `deploy/cr.yaml`:

```sh
kubectl apply -f https://raw.githubusercontent.com/percona/percona-server-mysql-operator/main/deploy/cr.yaml

```

# Need help?

**Commercial Support**  | **Community Support** |
:-: | :-: |
| <br/>Enterprise-grade assistance for your mission-critical MySQL deployments in Kubernetes. Get expert guidance for complex tasks like multi-cloud replication, database migration and building platforms.<br/><br/>  | <br/>Connect with our engineers and fellow users for general questions, troubleshooting, and sharing feedback and ideas.<br/><br/>  | 
| **[Get Percona Support](https://hubs.ly/Q02ZTHb-0)** | **[Visit our Forum](https://forums.percona.com/c/mysql-mariadb/percona-kubernetes-operator-for-mysql/28)** |


# Contributing

Percona welcomes and encourages community contributions to help improve Percona Operator for MySQL.

See the [Contribution Guide](CONTRIBUTING.md) and [Building and Testing Guide](e2e-tests/README.md) for more information on how you can contribute.

## Roadmap

We have an experimental public roadmap which can be found [here](https://github.com/percona/roadmap/projects/1). Please feel free to contribute and propose new features by following the roadmap [guidelines](https://github.com/percona/roadmap).

## Submitting Bug Reports

If you find a bug in Percona Docker Images or in one of the related projects, please submit a report to that project's [JIRA](https://jira.percona.com/browse/K8SPS) issue tracker or [create a GitHub issue](https://docs.github.com/en/issues/tracking-your-work-with-issues/creating-an-issue#creating-an-issue-from-a-repository) in this repository. 

Learn more about submitting bugs, new features ideas and improvements in the [Contribution Guide](CONTRIBUTING.md).
