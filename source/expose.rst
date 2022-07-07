Exposing cluster
================

The Operator provides entry points for accessing the database by client
applications in several scenarios. In either way the cluster is exposed with
regular Kubernetes `Service objects<https://kubernetes.io/docs/concepts/services-networking/service/>`_,
configured by the Operator.

This document describes the usage of :ref:`Custom Resource manifest options<operator.custom-resource-options>`
to expose the clusters deployed with the Operator. The expose options vary for
different replication types: `Asyncronous<https://dev.mysql.com/doc/refman/8.0/en/replication.html>`_
and `Group Replicaiton <https://dev.mysql.com/doc/refman/8.0/en/group-replication.html>`_.

Asyncronous Replication
-----------------------

With `Asyncronous or Semi-syncronous replication <https://dev.mysql.com/doc/refman/8.0/en/group-replication-primary-secondary-replication.html>`_
the cluster is exposed through a Kubernetes Service called
``<CLUSTER_NAME>-mysql-primary``: for example, ``cluster1-mysql-primary``.

.. image:: ./assets/images/exposure-async.svg
   :align: center

This Service is created by default and is always present. You can change the
type of the Service object by setting :ref:`mysql.primaryServiceType<mysql-primaryservicetype>`
variable in the Custom Resource.

The following example exposes the Primary node of the asyncronous cluster with
the LoadBalancer object:

.. code:: yaml

   mysql:
     clusterType: async
     ...
     primaryServiceType: LoadBalancer

When the cluster is configured in this way, you can find the LoadBalancer
endpoint or IP-address by getting the Service object with
``kubectl get service`` command:

.. code:: bash

   $ kubectl get service cluster1-mysql-primary
   NAME                     TYPE           CLUSTER-IP     EXTERNAL-IP     PORT(S)                                                         AGE
   cluster1-mysql-primary   LoadBalancer   10.40.37.98    35.192.172.85   3306:32146/TCP,33062:31062/TCP,33060:32026/TCP,6033:30521/TCP   3m31s

Group Replication
-----------------

Clusters configured to use Group Replication are exposed via the `MySQL Router <https://dev.mysql.com/doc/mysql-router/8.0/en/>`_ proxy.
Network design in this case looks like this:

.. image:: ./assets/images/exposure-gr.svg
   :align: center

MySQL Router can be configured via the :ref:`router section<operator.router-section>`.
In particular, the :ref:`router.expose.type<router-expose-type>` option sets the
type of the correspondent Kubernetes Service object. The following example
exposes MySQL Router through a LoadBalancer object:

.. code:: yaml

   mysql:
     clusterType: group-replication
     ...
   router:
     expose:
       type: LoadBalancer

When the cluster is configured in this way, you can find the endpoint to
connect to by getting the output of ``PerconaServerMySQL`` object by
``kubectl get ps`` command:

.. code:: bash

   $ kubectl get ps
   NAME       REPLICATION         ENDPOINT        STATE   AGE
   cluster1   group-replication   35.239.63.143   ready   10m

Service per Pod
---------------

Still, sometimes it is required to expose all MySQL instances, where each of
them gets its own IP address. This is possible by setting the following options in :ref:`spec.mysql section<operator.mysql-section>`.

* :ref:`mysql.expose.enabled<mysql-expose-enabled>` enables or disables exposure
  of MySQL instances,
* :ref:`mysql.expose.type<mysql-expose-type>` defines the Kubernetes Service
  object type.

The following example creates a dedicated LoadBalancer Service for each node of
the MySQL cluster:

.. code:: yaml

   mysql:
     expose:
       enabled: true
       type: LoadBalancer

When the cluster instances are exposed in this way, you can find the
corresponding Services with ``kubectl get services`` command:

.. code:: bash

   $ kubectl get services
   NAME                     TYPE           CLUSTER-IP     EXTERNAL-IP     PORT(S)                                                         AGE
   ...
   cluster1-mysql-0         LoadBalancer   10.40.44.110   104.198.16.21   3306:31009/TCP,33062:31319/TCP,33060:30737/TCP,6033:30660/TCP   75s
   cluster1-mysql-1         LoadBalancer   10.40.42.5     34.70.170.187   3306:30601/TCP,33062:30273/TCP,33060:30910/TCP,6033:30847/TCP   75s
   cluster1-mysql-2         LoadBalancer   10.40.42.158   35.193.50.44    3306:32042/TCP,33062:31576/TCP,33060:31656/TCP,6033:31448/TCP   75s
