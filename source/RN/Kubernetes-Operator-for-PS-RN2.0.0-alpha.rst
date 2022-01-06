.. rn:: 2.0.0-alpha

*Percona Distribution for MySQL Operator* 2.0.0-alpha
==============================================================

Kubernetes provides users with a distributed orchestration system that automates
the deployment, management, and scaling of containerized applications. The
Operator extends the Kubernetes API with a new custom resource for deploying,
configuring, and managing the application through the whole life cycle.
You can compare the Kubernetes Operator to a System Administrator who deploys
the application and watches the Kubernetes events related to it, taking
administrative/operational actions when needed.

The already existing `Percona Distribution for MySQL Operator <https://www.percona.com/doc/kubernetes-operator-for-pxc/index.html>`_ is based on Percona XtraDB Cluster. It is feature rich and provides virtually-synchronous replication by utilizing Galera Write-Sets. Sync replication ensures data consistency and proved itself useful for critical applications, especially on Kubernetes.

The new *Percona Distribution for MySQL Operator 2* is going to run Percona Server for MySQL and provide both regular asynchronous (with `semi-sync <https://dev.mysql.com/doc/refman/5.7/en/replication-semisync.html>`_ support) and virtually-synchronous replication based on `Group Replication <https://dev.mysql.com/doc/refman/8.0/en/group-replication.html>`_.

**Version 2.0.0-alpha of the Percona Distribution for MySQL Operator is a tech preview release and it is not recommended for production environments.**

You can install *Percona Distribution for MySQL Operator* on Kubernetes,
`Google Kubernetes Engine (GKE) <https://cloud.google.com/kubernetes-engine>`_,
`Amazon Elastic Container Service for Kubernetes (EKS) <https://aws.amazon.com>`_,
and `Minikube <https://minikube.sigs.k8s.io/docs/>`_.

The features available in this release are the following:

* Deploy asynchronous and semi-sync replication MySQL clusters with Orchestrator on top of it,
* Expose the cluster with regular Kubernetes Services,
* Monitor the cluster with Percona Monitoring and Management,
* Customize MySQL configuration,
* Rotate system user passwords,
* Customize MySQL Pods with sidecar containers.

Installation
------------

Installation is performed by following the documentation installation instructions for :ref:`Kubernetes<install-kubernetes>`, :ref:`Amazon Elastic Kubernetes Service<install-eks>` and :ref:`Minikube<install-minikube>`.
