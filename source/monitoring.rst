.. _operator.monitoring:

Monitoring
==========

Percona Monitoring and Management (PMM) `provides an excellent
solution <https://www.percona.com/doc/percona-xtradb-cluster/LATEST/manual/monitoring.html#using-pmm>`_
to monitor Percona Distribution for MySQL.

.. note:: Only PMM 2.x versions are supported by the Operator.

PMM is a client/server application. *PMM Client* runs on each node with the
database you wish to monitor: it collects needed metrics and sends gathered data
to *PMM Server*. As a user, you connect to PMM Server to see database metrics on
a number of dashboards.

That's why PMM Server and PMM Client need to be installed separately.

Installing the PMM Server
-------------------------

PMM Server runs as a *Docker image*, a *virtual appliance*, or on an *AWS instance*.
Please refer to the `official PMM documentation <https://www.percona.com/doc/percona-monitoring-and-management/2.x/setting-up/server/index.html>`_
for the installation instructions.

Installing the PMM Client
-------------------------

The following steps are needed for the PMM client installation in your
Kubernetes-based environment:

#. The PMM client installation is initiated by updating the ``pmm``
   section in the
   `deploy/cr.yaml <https://github.com/percona/percona-server-mysql-operator/blob/main/deploy/cr.yaml>`_
   file.

   -  set ``pmm.enabled=true``
   -  set the ``pmm.serverHost`` key to your PMM Server hostname,
   -  check that  the ``serverUser`` key contains your PMM Server user name
      (``admin`` by default),
   -  make sure the ``pmmserver`` key in the 
      `deploy/secrets.yaml <https://github.com/percona/percona-server-mongodb-operator/blob/main/deploy/secrets.yaml>`_
      secrets file contains the password specified for the PMM Server during its
      installation

      .. note:: You use ``deploy/secrets.yaml`` file to *create* Secrets Object.
         The file contains all values for each key/value pair in a convenient
         plain text format. But the resulting Secrets contain passwords stored
         as base64-encoded strings. If you want to *update* password field,
         you'll need to encode the value into base64 format. To do this, you can
         run ``echo -n "password" | base64`` in your local shell to get valid
         values. For example, setting the PMM Server user's password to 
         `new_password`` in the ``cluster1-secrets`` object can be done
         with the following command:

         .. code:: bash

            kubectl patch secret/cluster1-secrets -p '{"data":{"pmmserver": '$(echo -n new_password | base64)'}}'

   Apply changes with the ``kubectl apply -f deploy/secrets.yaml`` command.

   When done, apply the edited ``deploy/cr.yaml`` file:

   .. code:: bash

      $ kubectl apply -f deploy/cr.yaml

#. Check that corresponding Pods are not in a cycle of stopping and restarting.
   This cycle occurs if there are errors on the previous steps:

   .. code:: bash
   
      $ kubectl get pods
      $ kubectl logs cluster1-mysql-0 -c pmm-client

#. Now you can access PMM via *https* in a web browser, with the
   login/password authentication, and the browser is configured to show
   Percona Server for MySQL metrics.
