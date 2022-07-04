Exposing cluster
================

The cluster can be exposed with regular Kubernetes `Service <https://kubernetes.io/docs/concepts/services-networking/service/>`_ objects. 
Operator takes care of configuring Service objects. 

This document describes the usage of Custom Resource manifest options 
to expose the clusters deployed with the Operator. Expose options vary for
different replication types: Asyncronous and `Group Replicaiton <https://dev.mysql.com/doc/refman/8.0/en/group-replication.html>`_.


Asyncronous Replication
-----------------------

TBD

Group Replication
-----------------

`MySQL Router <https://dev.mysql.com/doc/mysql-router/8.0/en/>`_  is used to expose clusters with Group Replication. 
Network design in this case looks like this:

picture here

To configure MySQL Router use `router` section, and for exposure - `router.expose`.

Set `router.expose.type` to specify Kubernetes Service object. The following example
is going to expose MySQL Router through a LoadBalancer object:

.. code:: yaml

  router:
    expose:
      type: LoadBalancer


Get the endpoint to connect to by getting the output of PerconaServerMySQL object:

.. code:: bash

  $ kubectl get ps
  NAME       REPLICATION         ENDPOINT        STATE   AGE
  cluster1   group-replication   35.239.63.143   ready   10m
