# Percona Operator for MySQL

[![License](https://img.shields.io/badge/License-Apache%202.0-blue.svg)](https://opensource.org/licenses/Apache-2.0)

[Percona Server for MySQL](https://www.percona.com/software/mysql-database/percona-server) is a free, fully compatible, enhanced, and open source drop-in replacement for any MySQL database. It provides superior performance, scalability, and instrumentation.

Based on our best practices for deployment and configuration, [Percona Operator for MySQL](https://www.percona.com/doc/kubernetes-operator-for-mysql/ps/index.html) contains everything you need to quickly and consistently deploy and scale MySQL instances in a Kubernetes-based environment on-premises or in the cloud. It provides the following capabilities:

* Deploy asynchronous and semi-sync replication MySQL clusters with Orchestrator on top of it
* Expose clusters with regular Kubernetes Services
* Monitor the cluster with [Percona Monitoring and Management](https://www.percona.com/software/database-tools/percona-monitoring-and-management)
* Customize MySQL configuration
* Manage system user passwords

## Status

**This project is in the tech preview state right now. Don't use it on production.**

## Roadmap

Our goal is to release the GA version late in Q2’2022. We plan to include the following:

* Support for both sync and async replication
* Backups and restores, proxies integration
* Exceptional quality and full test coverage
* Certifications on various Kubernetes platforms and flavors

## Architecture

Percona Operators are based on the [Operator SDK](https://github.com/operator-framework/operator-sdk) and leverage Kubernetes primitives to follow best CNCF practices.

## Installation

It usually takes two steps to deploy Percona Server for MySQL on Kubernetes:

* Deploy the operator from `deploy/bundle.yaml`
* Deploy the database cluster itself from `deploy/cr.yaml`

See full documentation with examples and various advanced cases on [percona.com](https://www.percona.com/doc/kubernetes-operator-for-mysql/ps/index.html).

## Contributing

Percona welcomes and encourages community contributions to help improve open source software.

See the [Contribution Guide](CONTRIBUTING.md) for more information.

## Submitting Bug Reports

If you find a bug in Percona Docker Images or in one of the related projects, please submit a report to that project's [JIRA](https://jira.percona.com/browse/K8SPS) issue tracker. Learn more about submitting bugs, new features ideas and improvements in the [Contribution Guide](CONTRIBUTING.md).

