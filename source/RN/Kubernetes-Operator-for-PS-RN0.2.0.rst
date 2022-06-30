.. rn:: 0.2.0

================================================================================
*Percona Operator for MySQL* 0.2.0
================================================================================

:Date: June 30, 2022
:Installation: `Installing Percona Operator for MySQL <https://www.percona.com/doc/kubernetes-operator-for-mysql/ps/index.html#advanced-installation-guides>`_

.. note:: Version 0.2.0 of the Percona Operator for MySQL is **a tech preview release** and it is **not recommended for production environments**. **As of today, we recommend using** `Percona Operator for MySQL based on Percona XtraDB Cluster <https://www.percona.com/doc/kubernetes-operator-for-pxc/index.html>`_, which is production-ready and contains everything you need to quickly and consistently deploy and scale MySQL clusters in a Kubernetes-based environment, on-premises or in the cloud.

Release Highlights
================================================================================

* With this release, the Operator turns to a simplified naming convention and
  changes its official name to **Percona Operator for MySQL**
* This release brings initial :ref:`implementation of Group Replication<mysql-clustertype>` between Percona Server for MySQL instances. Group Replication works in conjunction with MySQL Router, which is used instead of Orchestrator and also provides load balancing
* Now the Operator :ref:`is capable of making backups<backups>`. Backups are stored on the cloud outside the Kubernetes cluster: `Amazon S3, or S3-compatible storage <https://en.wikipedia.org/wiki/Amazon_S3#S3_API_and_competing_services>`_ is supported, as well as `Azure Blob Storage <https://azure.microsoft.com/en-us/services/storage/blobs>`_. Currently, backups are work with asynchronous replication; support for backups with Group Replication is coming

New Features
================================================================================

* :jirabug:`K8SPS-32`: Orchestrator is now highly available, allowing you to deploy a cluster without a single point of failure
* :jirabug:`K8SPS-53` and :jirabug:`K8SPS-54`: You can now backup and restore your MySQL database with the Operator
* :jirabug:`K8SPS-55` and :jirabug:`K8SPS-82`: Add Group Replication support and deploy MySQL Router proxy for load-balancing the traffic
* :jirabug:`K8SPS-56`: Automatically tune ``buffer_pool_size`` and ``max_connections`` options based on the resources provisioned for MySQL container if custom MySQL config is not provided

Improvements
================================================================================

* :jirabug:`K8SPS-39`: Show endpoint in the Custom Resource status to quickly identify endpoint URI, or public IP address in case of the LoadBalancer
* :jirabug:`K8SPS-47`: Expose MySQL Administrative Connection Port and MySQL Server X Protocol in Services

Bugs Fixed
================================================================================

* :jirabug:`K8SPS-58`: Fix a bug that caused cluster failure if MySQL initialization took longer than the startup probe delay
* :jirabug:`K8SPS-70`: Fix a bug that caused cluster crash if secretsName option was changed to another Secrets object with different passwords
* :jirabug:`K8SPS-78`: Make the Operator throw an error at cluster creation time if the storage is not specified

Supported Platforms 
================================================================================

The following platforms were tested and are officially supported by the Operator
0.2.0:

* `Google Kubernetes Engine (GKE) <https://cloud.google.com/kubernetes-engine>`_ 1.21 - 1.23
* `Amazon Elastic Container Service for Kubernetes (EKS) <https://aws.amazon.com>`_ 1.19 - 1.22
* `Minikube <https://minikube.sigs.k8s.io/docs/>`_ 1.26 (based on Kubernetes 1.24)

This list only includes the platforms that the Percona Operators are specifically tested on as part of the release process. Other Kubernetes flavors and versions depend on backward compatibility offered by Kubernetes itself.
