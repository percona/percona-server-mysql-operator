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

.. note:: Version 0.2.0 of the `Percona Operator for MySQL <https://github.com/percona/percona-server-mysql-operator>`_ is **a tech preview release** and it is **not recommended for production environments**. **As of today, we recommend using** `Percona Operator for MySQL based on Percona XtraDB Cluster <https://www.percona.com/doc/kubernetes-operator-for-pxc/index.html>`_, which is production-ready and contains everything you need to quickly and consistently deploy and scale MySQL clusters in a Kubernetes-based environment, on-premises or in the cloud.

Requirements
============

.. toctree::
   :maxdepth: 1

   System-Requirements
   architecture

Installation guides
===================

.. toctree::
   :maxdepth: 1

   minikube
   kubernetes
   eks

Configuration and Management
============================

.. toctree::
   :maxdepth: 1

   users
   constraints
   options
   TLS
   scaling
   monitoring
   sidecar

.. toctree::
   :maxdepth: 1

Reference
=============

.. toctree::
   :maxdepth: 1

   operator
   images
   Release Notes <RN/index>
