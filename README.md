# Percona Operator for MySQL

[![License](https://img.shields.io/badge/License-Apache%202.0-blue.svg)](https://opensource.org/licenses/Apache-2.0)
![Docker Pulls](https://img.shields.io/docker/pulls/percona/percona-server-mysql-operator)
![Docker Image Size (latest by date)](https://img.shields.io/docker/image-size/percona/percona-server-mysql-operator)
![GitHub tag (latest by date)](https://img.shields.io/github/v/tag/percona/percona-server-mysql-operator)
![GitHub go.mod Go version](https://img.shields.io/github/go-mod/go-version/percona/percona-server-mysql-operator)
[![Go Report Card](https://goreportcard.com/badge/github.com/percona/percona-server-mysql-operator)](https://goreportcard.com/report/github.com/percona/percona-server-mysql-operator)

[Percona Server for MySQL](https://www.percona.com/software/mysql-database/percona-server) is a free, fully compatible, enhanced, and open source drop-in replacement for any MySQL database. It provides superior performance, scalability, and instrumentation.

Based on our best practices for deployment and configuration, [Percona Operator for MySQL](https://www.percona.com/doc/kubernetes-operator-for-mysql/ps/index.html) contains everything you need to quickly and consistently deploy and scale MySQL instances in a Kubernetes-based environment on-premises or in the cloud. It provides the following capabilities:

* Deploy asynchronous and semi-sync replication MySQL clusters with Orchestrator on top of it
* Expose clusters with regular Kubernetes Services
* Monitor the cluster with [Percona Monitoring and Management](https://www.percona.com/software/database-tools/percona-monitoring-and-management)
* Customize MySQL configuration
* Manage system user passwords

## Status

**This project is in the tech preview state right now. Don't use it on production.**

As of today, we recommend using [Percona Operator for MySQL based on Percona XtraDB Cluster](https://docs.percona.com/percona-operator-for-mysql/pxc/index.html), which is production-ready and contains everything you need to quickly and consistently deploy and scale MySQL clusters in a Kubernetes-based environment, on-premises or in the cloud.

## Roadmap

We have an experimental public roadmap which can be found [here](https://github.com/percona/roadmap/projects/1). Please feel free to contribute and propose new features by following the roadmap [guidelines](https://github.com/percona/roadmap).

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

## Join Percona Kubernetes Squad!
                                                                              
```                                                                                     
                    %                        _____                
                   %%%                      |  __ \                                          
                 ###%%%%%%%%%%%%*           | |__) |__ _ __ ___ ___  _ __   __ _             
                ###  ##%%      %%%%         |  ___/ _ \ '__/ __/ _ \| '_ \ / _` |            
              ####     ##%       %%%%       | |  |  __/ | | (_| (_) | | | | (_| |            
             ###        ####      %%%       |_|   \___|_|  \___\___/|_| |_|\__,_|           
           ,((###         ###     %%%        _      _          _____                       _
          (((( (###        ####  %%%%       | |   / _ \       / ____|                     | | 
         (((     ((#         ######         | | _| (_) |___  | (___   __ _ _   _  __ _  __| | 
       ((((       (((#        ####          | |/ /> _ </ __|  \___ \ / _` | | | |/ _` |/ _` |
      /((          ,(((        *###         |   <| (_) \__ \  ____) | (_| | |_| | (_| | (_| |
    ////             (((         ####       |_|\_\\___/|___/ |_____/ \__, |\__,_|\__,_|\__,_|
   ///                ((((        ####                                  | |                  
 /////////////(((((((((((((((((########                                 |_|   Join @ percona.com/k8s                                                              
```

You can get early access to new product features, invite-only ”ask me anything” sessions with Percona Kubernetes experts, and monthly swag raffles. Interested? Fill in the form at [percona.com/k8s](https://www.percona.com/k8s).

## Submitting Bug Reports

If you find a bug in Percona Docker Images or in one of the related projects, please submit a report to that project's [JIRA](https://jira.percona.com/browse/K8SPS) issue tracker. Learn more about submitting bugs, new features ideas and improvements in the [Contribution Guide](CONTRIBUTING.md).

