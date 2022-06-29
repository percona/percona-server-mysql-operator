Percona Operator for MySQL
=========================================================================

Kubernetes have added a way to
manage containerized systems, including database clusters. This management is
achieved by controllers, declared in configuration files. These controllers
provide automation with the ability to create objects, such as a container or a
group of containers called Pods, to listen for an specific event and then
perform a task.

This automation adds a level of complexity to the container-based architecture
and stateful applications, such as a database. A Kubernetes Operator is a
special type of controller introduced to simplify complex deployments. The
Operator extends the Kubernetes API with custom resources.

`Percona Server for MySQL <https://www.percona.com/doc/percona-server/LATEST/index.html>`_
is a free, fully compatible, enhanced, and open source drop-in replacement for
any MySQL database. It provides superior performance, scalability, and
instrumentation.
Based on our best practices for deployment and configuration, `Percona Operator for MySQL <https://github.com/percona/percona-server-mysql-operator>`_
contains everything you need to quickly and consistently deploy and scale MySQL
instances in a Kubernetes-based environment on-premises or in the cloud.

Requirements
============

.. toctree::
   :maxdepth: 1

   System Requirements <System-Requirements>
   Design and architecture <architecture>

Quickstart guides
=================

.. toctree::
   :maxdepth: 1

   Install with Helm <helm.rst>
   Install on Minikube <minikube.rst>
   Install on Amazon Elastic Kubernetes Service (AWS EKS) <eks.rst>

Installation guides
===================

.. toctree::
   :maxdepth: 1

   Generic Kubernetes installation <kubernetes.rst>

Configuration and Management
============================

.. toctree::
   :maxdepth: 1

   Application and system users <users.rst>
   Anti-affinity and tolerations <constraints.rst>
   Changing MySQL Options <options.rst>
   Transport Encryption (TLS/SSL) <TLS.rst>
   Horizontal and vertical scaling <scaling.rst>
   Monitor with Percona Monitoring and Management (PMM) <monitoring.rst>
   Add sidecar containers <sidecar.rst>

.. toctree::
   :maxdepth: 1

Reference
=============

.. toctree::
   :maxdepth: 1

   Custom Resource options <operator.rst>
   Percona certified images <images.rst>
   Release Notes <RN/index>
