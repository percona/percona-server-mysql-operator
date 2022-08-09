.. _install-helm:

Install Percona Server for MySQL using Helm
===========================================

`Helm <https://github.com/helm/helm>`_ is the package manager for Kubernetes. Percona Helm charts can be found in `percona/percona-helm-charts <https://github.com/percona/percona-helm-charts>`_ repository on Github.

Pre-requisites
--------------

Install Helm following its `official installation instructions <https://docs.helm.sh/using_helm/#installing-helm>`_.

.. note:: Helm v3 is needed to run the following steps.


Installation
-------------

#. Add the Percona's Helm charts repository and make your Helm client up to
   date with it:

   .. code:: bash

      $ helm repo add percona https://percona.github.io/percona-helm-charts/
      $ helm repo update

#. Install the Percona Operator for MySQL:

   .. code:: bash

      $ helm install my-op percona/ps-operator

   The ``my-op`` parameter in the above example is the name of `a new release object <https://helm.sh/docs/intro/using_helm/#three-big-concepts>`_ 
   which is created for the Operator when you install its Helm chart (use any
   name you like).

   .. note:: If nothing explicitly specified, ``helm install`` command will work
      with ``default`` namespace. To use different namespace, provide it with
      the following additional parameter: ``--namespace my-namespace``.

#. Install Percona Server for MySQL:

   .. code:: bash

      $ helm install my-db percona/ps-db

   The ``my-db`` parameter in the above example is the name of `a new release object <https://helm.sh/docs/intro/using_helm/#three-big-concepts>`_ 
   which is created for the Percona Server for MySQL when you install its Helm
   chart (use any name you like).

.. _install-helm-params:

Installing Percona Server for MySQL with customized parameters
----------------------------------------------------------------

The command above installs Percona Server for MySQL with :ref:`default parameters<operator.custom-resource-options>`.
Custom options can be passed to a ``helm install`` command as a
``--set key=value[,key=value]`` argument. The options passed with a chart can be
any of the `Custom Resource options <https://github.com/percona/percona-helm-charts/tree/main/charts/ps-db#installing-the-chart>`_.

The following example will deploy a Percona Server for MySQL in the
``my-namespace`` namespace, with disabled backups and 20 Gi storage:

.. code:: bash

   $ helm install my-db percona/ps-db --namespace my-namespace \
     --set mysql.volumeSpec.pvc.resources.requests.storage=20Gi \
     --set backup.enabled=false

