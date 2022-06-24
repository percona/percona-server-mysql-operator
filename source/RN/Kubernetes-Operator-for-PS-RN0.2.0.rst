.. rn:: 0.2.0

================================================================================
*Percona Operator for MySQL* 0.2.0
================================================================================

:Date: June 30, 2022
:Installation: `Installing Percona Operator for MySQL <https://www.percona.com/doc/kubernetes-operator-for-mysql/ps/index.html#advanced-installation-guides>`_

.. note:: Version 0.2.0 of the Percona Operator for MySQL is **a tech preview release** and it is **not recommended for production environments**. **As of today, we recommend using** `Percona Operator for MySQL based on Percona XtraDB Cluster <https://www.percona.com/doc/kubernetes-operator-for-pxc/index.html>`_, which is production-ready and contains everything you need to quickly and consistently deploy and scale MySQL instances in a Kubernetes-based environment on-premises or in the cloud.

Release Highlights
================================================================================

* With this release, the Operator turns to a simplified naming convention and
  changes its official name to **Percona Operator for MySQL**

New Features
================================================================================

* :jirabug:`K8SPS-32`: Run orchestrator with high availability Tomislav Plavcic In QA Under Review 
* :jirabug:`K8SPS-53`: Add backups - MVP Tomislav Plavcic In QA 27 commits 
* :jirabug:`K8SPS-54`: Add restores - MVP Ege Gunes Under Review 
* :jirabug:`K8SPS-55`: Add group replication support Ege Gunes In Progress Under Review 
* :jirabug:`K8SPS-56`: Auto tune MySQL parameters using container resources Andrii Dema In Progress Under Review 
* :jirabug:`K8SPS-82`: Deploy MySQL Router Ege Gunes Open Under Review 

Improvements
================================================================================

* :jirabug:`K8SPS-39`: add endpoint to cr status Andrii Dema
* :jirabug:`K8SPS-41`: Use super_read_only variable Andrii Dema
* :jirabug:`K8SPS-47`: Expose more mysql ports in services Andrii Dema

Bugs Fixed
================================================================================

* :jirabug:`K8SPS-100`: data not available in all nodes after restore Ege Gunes
* :jirabug:`K8SPS-58`: Cluster fails if MySQL initialization takes longer than startup probe delay seconds
* :jirabug:`K8SPS-70`: cluster crashes if secretsName is changed (together with passwords)
* :jirabug:`K8SPS-72`: Can't change service type for primary and replica services
* :jirabug:`K8SPS-78`: operator should throw an error if storage is not specified
* :jirabug:`K8SPS-84`: old master cannot rejoin cluster because of super read only
* :jirabug:`K8SPS-85`: Fix false positives in test report
* :jirabug:`K8SPS-99`: cluster cannot start after second restore
* :jirabug:`K8SPS-65`: Updating replication password breaks replication with semi-sync replicas

Supported Platforms 
================================================================================

The following platforms were tested and are officially supported by the Operator
0.2.0:

* `Google Kubernetes Engine (GKE) <https://cloud.google.com/kubernetes-engine>`_ 1.20 - {{{gkerecommended}}}
* `Amazon Elastic Container Service for Kubernetes (EKS) <https://aws.amazon.com>`_ 1.20 - 1.22
* `Minikube <https://minikube.sigs.k8s.io/docs/>`_ 1.23

This list only includes the platforms that the Percona Operators are specifically tested on as part of the release process. Other Kubernetes flavors and versions depend on the backward compatibility offered by Kubernetes itself.
