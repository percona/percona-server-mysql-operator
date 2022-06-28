.. _operator.custom-resource-options:

`Custom Resource options <operator.html#operator-custom-resource-options>`_
===============================================================================

Percona Server for MySQL managed by the Operator is configured via the spec section
of the `deploy/cr.yaml <https://github.com/percona/percona-server-mysql-operator/blob/main/deploy/cr.yaml>`__
file.

The metadata part of this file contains the following keys:

* ``name`` (``cluster1`` by default) sets the name of your Percona Server for
  MySQL cluster; it should include only `URL-compatible characters <https://datatracker.ietf.org/doc/html/rfc3986#section-2.3>`_,
  not exceed 22 characters, start with an alphabetic character, and end with an
  alphanumeric character;

The spec part of the `deploy/cr.yaml <https://github.com/percona/percona-server-mysql-operator/blob/main/deploy/cr.yaml>`__ file contains the following sections:

.. tabularcolumns:: |p{40mm}|p{10mm}|p{49mm}|p{47mm}|

.. list-table::
   :widths: 25 9 31 35
   :header-rows: 1

   * - Key
     - Value type
     - Default
     - Description

   * - mysql
     - :ref:`subdoc<operator.mysql-section>`
     -
     - Percona Server for MySQL general section

   * - orchestrator
     - :ref:`subdoc<operator.orchestrator-section>`
     -
     - Orchestrator section

   * - pmm
     - :ref:`subdoc<operator.pmm-section>`
     -
     - Percona Monitoring and Management section

   * - secretsName
     - string
     - ``cluster1-secrets``
     - A name for :ref:`users secrets<users>`

   * - sslSecretName
     - string
     - ``cluster1-ssl``
     - A secret with TLS certificate generated for *external* communications, see :ref:`tls` for details

.. _operator.mysql-section:

`Percona Server for MySQL Section <operator.html#operator-mysql-section>`_
--------------------------------------------------------------------------------

The ``mysql`` section in the `deploy/cr.yaml <https://github.com/percona/percona-server-mysql-operator/blob/main/deploy/cr.yaml>`__ file contains general
configuration options for the Percona Server for MySQL.

.. tabularcolumns:: |p{2cm}|p{13.6cm}|

+-----------------+-------------------------------------------------------------------------------------------+
|                 | .. _mysql-size:                                                                           |
|                 |                                                                                           |
| **Key**         | `mysql.size <operator.html#mysql-size>`_                                                  |
+-----------------+-------------------------------------------------------------------------------------------+
| **Value**       | int                                                                                       |
+-----------------+-------------------------------------------------------------------------------------------+
| **Example**     | ``3``                                                                                     |
+-----------------+-------------------------------------------------------------------------------------------+
| **Description** | The number of the Percona Server for MySQL instances                                      |
+-----------------+-------------------------------------------------------------------------------------------+
|                                                                                                             |
+-----------------+-------------------------------------------------------------------------------------------+
|                 | .. _mysql-image:                                                                          |
|                 |                                                                                           |
| **Key**         | `mysql.image <operator.html#mysql-image>`_                                                |
+-----------------+-------------------------------------------------------------------------------------------+
| **Value**       | string                                                                                    |
+-----------------+-------------------------------------------------------------------------------------------+
| **Example**     | ``percona/percona-server:{{{ps80recommended}}}``                                                         |
+-----------------+-------------------------------------------------------------------------------------------+
| **Description** | The Docker image of the Percona Server for MySQL used (actual image names for Percona     |
|                 | Server for MySQL 8.0 and Percona Server for MySQL 5.7 can be found                        |
|                 | :ref:`in the list of certified images<custom-registry-images>`)                           |
+-----------------+-------------------------------------------------------------------------------------------+
|                                                                                                             |
+-----------------+-------------------------------------------------------------------------------------------+
|                 | .. _mysql-imagepullsecrets-name:                                                          |
|                 |                                                                                           |
| **Key**         | `mysql.imagePullSecrets.name <operator.html#mysql-imagepullsecrets-name>`_                |
+-----------------+-------------------------------------------------------------------------------------------+
| **Value**       | string                                                                                    |
+-----------------+-------------------------------------------------------------------------------------------+
| **Example**     | ``private-registry-credentials``                                                          |
+-----------------+-------------------------------------------------------------------------------------------+
| **Description** | The `Kubernetes ImagePullSecret                                                           |
|                 | <https://kubernetes.io/docs/concepts/configuration/secret/#using-imagepullsecrets>`_      |
+-----------------+-------------------------------------------------------------------------------------------+
|                                                                                                             |
+-----------------+-------------------------------------------------------------------------------------------+
|                 | .. _mysql-sizesemisync:                                                                   |
|                 |                                                                                           |
| **Key**         | `mysql.sizeSemiSync <operator.html#mysql-sizesemisync>`_                                  |
+-----------------+-------------------------------------------------------------------------------------------+
| **Value**       | int                                                                                       |
+-----------------+-------------------------------------------------------------------------------------------+
| **Example**     | ``0``                                                                                     |
+-----------------+-------------------------------------------------------------------------------------------+
| **Description** | The number of the Percona Server for MySQL `semi-sync                                     |
|                 | <https://dev.mysql.com/doc/refman/8.0/en/replication-semisync.html>`_ replicas            |
+-----------------+-------------------------------------------------------------------------------------------+
|                                                                                                             |
+-----------------+-------------------------------------------------------------------------------------------+
|                 | .. _mysql-primaryServiceType:                                                             |
|                 |                                                                                           |
| **Key**         | `mysql.primaryServiceType <operator.html#mysql-primaryServiceType>`_                      |
+-----------------+-------------------------------------------------------------------------------------------+
| **Value Type**  | string                                                                                    |
+-----------------+-------------------------------------------------------------------------------------------+
| **Example**     | ``LoadBalancer``                                                                          |
+-----------------+-------------------------------------------------------------------------------------------+
| **Description** | Specifies the type of `Kubernetes Service                                                 |
|                 | <https://kubernetes.io/docs/concepts/services-networking/service/                         |
|                 | #publishing-services-service-types>`_ to be used for Primary instance                     |
+-----------------+-------------------------------------------------------------------------------------------+
|                                                                                                             |
+-----------------+-------------------------------------------------------------------------------------------+
|                 | .. _mysql-replicasServiceType:                                                            |
|                 |                                                                                           |
| **Key**         | `mysql.replicasServiceType <operator.html#mysql-replicasServiceType>`_                    |
+-----------------+-------------------------------------------------------------------------------------------+
| **Value Type**  | string                                                                                    |
+-----------------+-------------------------------------------------------------------------------------------+
| **Example**     | ``ClusterIP``                                                                             |
+-----------------+-------------------------------------------------------------------------------------------+
| **Description** | Specifies the type of `Kubernetes Service                                                 |
|                 | <https://kubernetes.io/docs/concepts/services-networking/service/                         |
|                 | #publishing-services-service-types>`_ to be used for Replica instances                    |
+-----------------+-------------------------------------------------------------------------------------------+
|                                                                                                             |
+-----------------+-------------------------------------------------------------------------------------------+
|                 | .. _mysql-resources-requests-memory:                                                      |
|                 |                                                                                           |
| **Key**         | `mysql.resources.requests.memory <operator.html#mysql-resources-requests-memory>`_        |
+-----------------+-------------------------------------------------------------------------------------------+
| **Value**       | string                                                                                    |
+-----------------+-------------------------------------------------------------------------------------------+
| **Example**     | ``512M``                                                                                  |
+-----------------+-------------------------------------------------------------------------------------------+
| **Description** | The `Kubernetes memory requests                                                           |
|                 | <https://kubernetes.io/docs/concepts/configuration/manage-compute-resources-container/    |
|                 | #resource-requests-and-limits-of-pod-and-container>`_                                     |
|                 | for a Percona Server for MySQL container                                                  |
+-----------------+-------------------------------------------------------------------------------------------+
|                                                                                                             |
+-----------------+-------------------------------------------------------------------------------------------+
|                 | .. _mysql-resources-limits-memory:                                                        |
|                 |                                                                                           |
| **Key**         | `mysql.resources.limits.memory <operator.html#mysql-resources-limits-memory>`_            |
+-----------------+-------------------------------------------------------------------------------------------+
| **Value**       | string                                                                                    |
+-----------------+-------------------------------------------------------------------------------------------+
| **Example**     | ``1G``                                                                                    |
+-----------------+-------------------------------------------------------------------------------------------+
| **Description** | `Kubernetes memory limits                                                                 |
|                 | <https://kubernetes.io/docs/concepts/configuration/manage-compute-resources-container/    |
|                 | #resource-requests-and-limits-of-pod-and-container>`_ for a Percona Server for MySQL      |
|                 | container                                                                                 |
+-----------------+-------------------------------------------------------------------------------------------+
|                                                                                                             |
+-----------------+-------------------------------------------------------------------------------------------+
|                 | .. _mysql-affinity-antiaffinitytopologykey:                                               |
|                 |                                                                                           |
| **Key**         | `mysql.affinity.antiAffinityTopologyKey                                                   |
|                 | <operator.html#mysql-affinity-antiAffinityTopologyKey>`_                                  |
+-----------------+-------------------------------------------------------------------------------------------+
| **Value**       | string                                                                                    |
+-----------------+-------------------------------------------------------------------------------------------+
| **Example**     | ``kubernetes.io/hostname``                                                                |
+-----------------+-------------------------------------------------------------------------------------------+
| **Description** | The Operator `topology key                                                                |
|                 | <https://kubernetes.io/docs/concepts/configuration/assign-pod-node/                       |
|                 | #affinity-and-anti-affinity>`_ node anti-affinity constraint                              |
+-----------------+-------------------------------------------------------------------------------------------+
|                                                                                                             |
+-----------------+-------------------------------------------------------------------------------------------+
|                 | .. _mysql-affinity-advanced:                                                              |
|                 |                                                                                           |
| **Key**         | `mysql.affinity.advanced <operator.html#mysql-affinity-advanced>`_                        |
+-----------------+-------------------------------------------------------------------------------------------+
| **Value**       | subdoc                                                                                    |
+-----------------+-------------------------------------------------------------------------------------------+
| **Example**     |                                                                                           |
+-----------------+-------------------------------------------------------------------------------------------+
| **Description** | In cases where the Pods require complex tuning the `advanced` option turns off the        |
|                 | ``topologyKey`` effect. This setting allows the standard Kubernetes affinity constraints  |
|                 | of any complexity to be used                                                              |
+-----------------+-------------------------------------------------------------------------------------------+
|                                                                                                             |
+-----------------+-------------------------------------------------------------------------------------------+
|                 | .. _mysql-expose-enabled:                                                                 |
|                 |                                                                                           |
| **Key**         | `mysql.expose.enabled <operator.html#mysql-expose-enabled>`_                              |
+-----------------+-------------------------------------------------------------------------------------------+
| **Value Type**  | boolean                                                                                   |
+-----------------+-------------------------------------------------------------------------------------------+
| **Example**     | ``true``                                                                                  |
+-----------------+-------------------------------------------------------------------------------------------+
| **Description** | Enable or disable exposing Percona Server for MySQL nodes with dedicated IP addresses     |
+-----------------+-------------------------------------------------------------------------------------------+
|                                                                                                             |
+-----------------+-------------------------------------------------------------------------------------------+
|                 | .. _mysql-expose-type:                                                                    |
|                 |                                                                                           |
| **Key**         | `mysql.expose.type <operator.html#mysql-expose-type>`_                                    |
+-----------------+-------------------------------------------------------------------------------------------+
| **Value Type**  | string                                                                                    |
+-----------------+-------------------------------------------------------------------------------------------+
| **Example**     | ``ClusterIP``                                                                             |
+-----------------+-------------------------------------------------------------------------------------------+
| **Description** | The `Kubernetes Service Type                                                              |
|                 | <https://kubernetes.io/docs/concepts/services-networking/service/                         |
|                 | #publishing-services-service-types>`_ used for xposure                                    |
+-----------------+-------------------------------------------------------------------------------------------+
|                                                                                                             |
+-----------------+-------------------------------------------------------------------------------------------+
|                 | .. _mysql-volumespec-persistentvolumeclaim-resources-requests-storage:                    |
|                 |                                                                                           |
| **Key**         | `mysql.volumeSpec.persistentVolumeClaim.resources.requests.storage                        |
|                 | <operator.html#mysql-volumespec-persistentvolumeclaim-resources-requests-storage>`_       |
+-----------------+-------------------------------------------------------------------------------------------+
| **Value**       | string                                                                                    |
+-----------------+-------------------------------------------------------------------------------------------+
| **Example**     | ``2Gi``                                                                                   |
+-----------------+-------------------------------------------------------------------------------------------+
| **Description** | The `Kubernetes PersistentVolumeClaim                                                     |
|                 | <https://kubernetes.io/docs/concepts/storage/persistent-volumes/#                         |
|                 | persistentvolumeclaims>`_ size for the Percona Server for MySQL                           |
+-----------------+-------------------------------------------------------------------------------------------+
|                                                                                                             |
+-----------------+-------------------------------------------------------------------------------------------+
|                 | .. _mysql-configuration:                                                                  |
|                 |                                                                                           |
| **Key**         | `mysql.configuration <operator.html#mysql-configuration>`_                                |
+-----------------+-------------------------------------------------------------------------------------------+
| **Value**       | string                                                                                    |
+-----------------+-------------------------------------------------------------------------------------------+
| **Example**     | ``|``                                                                                     |
|                 |                                                                                           |
|                 | ``[mysqld]``                                                                              |
|                 |                                                                                           |
|                 | ``max_connections=250``                                                                   |
+-----------------+-------------------------------------------------------------------------------------------+
| **Description** | The ``my.cnf`` file options to be passed to Percona Server for MySQL instances            |
+-----------------+-------------------------------------------------------------------------------------------+
|                                                                                                             |
+-----------------+-------------------------------------------------------------------------------------------+
|                 | .. _mysql-sidecars-image:                                                                 |
|                 |                                                                                           |
| **Key**         | `mysql.sidecars.image <operator.html#mysql-sidecars-image>`_                              |
+-----------------+-------------------------------------------------------------------------------------------+
| **Value Type**  | string                                                                                    |
+-----------------+-------------------------------------------------------------------------------------------+
| **Example**     | ``busybox``                                                                               |
+-----------------+-------------------------------------------------------------------------------------------+
| **Description** | Image for the :ref:`custom sidecar container<operator-sidecar>`                           |
|                 | for Percona Server for MySQL Pods                                                         |
+-----------------+-------------------------------------------------------------------------------------------+
|                                                                                                             |
+-----------------+-------------------------------------------------------------------------------------------+
|                 | .. _mysql-sidecars-command:                                                               |
|                 |                                                                                           |
| **Key**         | `mysql.sidecars.command <operator.html#mysql-sidecars-command>`_                          |
+-----------------+-------------------------------------------------------------------------------------------+
| **Value Type**  | array                                                                                     |
+-----------------+-------------------------------------------------------------------------------------------+
| **Example**     | ``["sleep", "30d"]``                                                                      |
+-----------------+-------------------------------------------------------------------------------------------+
| **Description** | Command for the :ref:`custom sidecar container<operator-sidecar>`                         |
|                 | for Percona Server for MySQL Pods                                                         |
+-----------------+-------------------------------------------------------------------------------------------+
|                                                                                                             |
+-----------------+-------------------------------------------------------------------------------------------+
|                 | .. _mysql-sidecars-name:                                                                  |
|                 |                                                                                           |
| **Key**         | `mysql.sidecars.name <operator.html#mysql-sidecars-name>`_                                |
+-----------------+-------------------------------------------------------------------------------------------+
| **Value Type**  | string                                                                                    |
+-----------------+-------------------------------------------------------------------------------------------+
| **Example**     | ``my-sidecar-1``                                                                          |
+-----------------+-------------------------------------------------------------------------------------------+
| **Description** | Name of the :ref:`custom sidecar container<operator-sidecar>`                             |
|                 | for Percona Server for MySQL Pods                                                         |
+-----------------+-------------------------------------------------------------------------------------------+
|                                                                                                             |
+-----------------+-------------------------------------------------------------------------------------------+
|                 | .. _mysql-sidecars-volumemounts-mountpath:                                                |
|                 |                                                                                           |
| **Key**         | `mysql.sidecars.volumeMounts.mountPath                                                    |
|                 | <operator.html#mysql-sidecars-volumemounts-mountpath>`_                                   |
+-----------------+-------------------------------------------------------------------------------------------+
| **Value Type**  | string                                                                                    |
+-----------------+-------------------------------------------------------------------------------------------+
| **Example**     | ``/volume1``                                                                              |
+-----------------+-------------------------------------------------------------------------------------------+
| **Description** | Mount path of the                                                                         |
|                 | :ref:`custom sidecar container<operator-sidecar>` volume                                  |
|                 | for Replica Set Pods                                                                      |
+-----------------+-------------------------------------------------------------------------------------------+
|                                                                                                             |
+-----------------+-------------------------------------------------------------------------------------------+
|                 | .. _mysql-sidecars-resources-requests-memory:                                             |
|                 |                                                                                           |
| **Key**         | `mysql.sidecars.resources.requests.memory <operator.html#                                 |
|                 | mysql-sidecars-resources-requests-memory>`_                                               |
+-----------------+-------------------------------------------------------------------------------------------+
| **Value**       | string                                                                                    |
+-----------------+-------------------------------------------------------------------------------------------+
| **Example**     | ``16M``                                                                                   |
+-----------------+-------------------------------------------------------------------------------------------+
| **Description** | The `Kubernetes memory requests                                                           |
|                 | <https://kubernetes.io/docs/concepts/configuration/manage-compute-resources-container/    |
|                 | #resource-requests-and-limits-of-pod-and-container>`_                                     |
|                 | for a Percona Server for MySQL sidecar container                                          |
+-----------------+-------------------------------------------------------------------------------------------+
|                                                                                                             |
+-----------------+-------------------------------------------------------------------------------------------+
|                 | .. _mysql-sidecars-volumemounts-name:                                                     |
|                 |                                                                                           |
| **Key**         | `mysql.sidecars.volumeMounts.name                                                         |
|                 | <operator.html#mysql-sidecars-volumemounts-name>`_                                        |
+-----------------+-------------------------------------------------------------------------------------------+
| **Value Type**  | string                                                                                    |
+-----------------+-------------------------------------------------------------------------------------------+
| **Example**     | ``sidecar-volume-claim``                                                                  |
+-----------------+-------------------------------------------------------------------------------------------+
| **Description** | Name of the                                                                               |
|                 | :ref:`custom sidecar container<operator-sidecar>` volume                                  |
|                 | for Replica Set Pods                                                                      |
+-----------------+-------------------------------------------------------------------------------------------+
|                                                                                                             |
+-----------------+-------------------------------------------------------------------------------------------+
|                 | .. _mysql-sidecarvolumes:                                                                 |
|                 |                                                                                           |
| **Key**         | `mysql.sidecarVolumes                                                                     |
|                 | <operator.html#mysql-sidecarvolumes>`_                                                    |
+-----------------+-------------------------------------------------------------------------------------------+
| **Value Type**  | subdoc                                                                                    |
+-----------------+-------------------------------------------------------------------------------------------+
| **Example**     |                                                                                           |
+-----------------+-------------------------------------------------------------------------------------------+
| **Description** | `Volume specification <https://kubernetes.io/docs/concepts/storage/volumes/>`__ for the   |
|                 | :ref:`custom sidecar container<operator-sidecar>` volume                                  |
|                 | for Percona Server for MySQL Pods                                                         |
+-----------------+-------------------------------------------------------------------------------------------+
|                                                                                                             |
+-----------------+-------------------------------------------------------------------------------------------+
|                 | .. _mysql-sidecarpvcs:                                                                    |
|                 |                                                                                           |
| **Key**         | `mysql.sidecarPVCs                                                                        |
|                 | <operator.html#mysql-sidecarpvcs>`_                                                       |
+-----------------+-------------------------------------------------------------------------------------------+
| **Value Type**  | subdoc                                                                                    |
+-----------------+-------------------------------------------------------------------------------------------+
| **Example**     |                                                                                           |
+-----------------+-------------------------------------------------------------------------------------------+
| **Description** | `Persistent Volume Claim                                                                  |
|                 | <https://v1-20.docs.kubernetes.io/docs/concepts/storage/persistent-volumes/>`__ for the   |
|                 | :ref:`custom sidecar container<operator-sidecar>` volume                                  |
|                 | for Replica Set Pods                                                                      |
+-----------------+-------------------------------------------------------------------------------------------+

.. _operator.router-section:

`Router Section <operator.html#operator-router-section>`_
--------------------------------------------------------------------------------

The ``router`` section in the `deploy/cr.yaml <https://github.com/percona/percona-server-mysql-operator/blob/main/deploy/cr.yaml>`__ file contains configuration options for the `MySQL Router <https://dev.mysql.com/doc/mysql-router/8.0/en/>`_.

.. tabularcolumns:: |p{2cm}|p{13.6cm}|


+-----------------+-------------------------------------------------------------------------------------------+
|                 | .. _router-size:                                                                          |
|                 |                                                                                           |
| **Key**         | `router.size <operator.html#router-size>`_                                                |
+-----------------+-------------------------------------------------------------------------------------------+
| **Value**       | int                                                                                       |
+-----------------+-------------------------------------------------------------------------------------------+
| **Example**     | ``3``                                                                                     |
+-----------------+-------------------------------------------------------------------------------------------+
| **Description** | The number of the Router Pods to provide routing to MySQL Servers                         |
+-----------------+-------------------------------------------------------------------------------------------+
|                                                                                                             |
+-----------------+-------------------------------------------------------------------------------------------+
|                 | .. _router-image:                                                                         |
|                 |                                                                                           |
| **Key**         | `router.image <operator.html#router-image>`_                                              |
+-----------------+-------------------------------------------------------------------------------------------+
| **Value**       | string                                                                                    |
+-----------------+-------------------------------------------------------------------------------------------+
| **Example**     | ``perconalab/percona-server-mysql-operator:main-router``                                  |
+-----------------+-------------------------------------------------------------------------------------------+
| **Description** | Router Docker image to use                                                                |
+-----------------+-------------------------------------------------------------------------------------------+
|                                                                                                             |
+-----------------+-------------------------------------------------------------------------------------------+
|                 | .. _router-imagepullpolicy:                                                               |
|                 |                                                                                           |
| **Key**         | `router.imagePullPolicy <operator.html#router-imagepullpolicy>`_                          |
+-----------------+-------------------------------------------------------------------------------------------+
| **Value**       | string                                                                                    |
+-----------------+-------------------------------------------------------------------------------------------+
| **Example**     | ``Always``                                                                                |
+-----------------+-------------------------------------------------------------------------------------------+
| **Description** | The `policy used to update images <https://kubernetes.io/docs/concepts/containers/images/ |
|                 | #updating-images>`_                                                                       |
+-----------------+-------------------------------------------------------------------------------------------+
|                                                                                                             |
+-----------------+-------------------------------------------------------------------------------------------+
|                 | .. _router-affinity-antiaffinitytopologykey:                                              |
|                 |                                                                                           |
| **Key**         | `router.affinity.antiAffinityTopologyKey                                                  |
|                 | <operator.html#router-affinity-antiAffinityTopologyKey>`_                                 |
+-----------------+-------------------------------------------------------------------------------------------+
| **Value**       | string                                                                                    |
+-----------------+-------------------------------------------------------------------------------------------+
| **Example**     | ``kubernetes.io/hostname``                                                                |
+-----------------+-------------------------------------------------------------------------------------------+
| **Description** | The Operator `topology key                                                                |
|                 | <https://kubernetes.io/docs/concepts/configuration/assign-pod-node/                       |
|                 | #affinity-and-anti-affinity>`_ node anti-affinity constraint                              |
+-----------------+-------------------------------------------------------------------------------------------+
|                                                                                                             |
+-----------------+-------------------------------------------------------------------------------------------+
|                 | .. _router-affinity-advanced:                                                             |
|                 |                                                                                           |
| **Key**         | `router.affinity.advanced                                                                 |
|                 | <operator.html#router-affinity-advanced>`_                                                |
+-----------------+-------------------------------------------------------------------------------------------+
| **Value**       | subdoc                                                                                    |
+-----------------+-------------------------------------------------------------------------------------------+
| **Example**     |                                                                                           |
+-----------------+-------------------------------------------------------------------------------------------+
| **Description** | In cases where the Pods require complex tuning the `advanced` option turns off the        |
|                 | ``topologyKey`` effect. This setting allows the standard Kubernetes affinity constraints  |
|                 | of any complexity to be used                                                              |
+-----------------+-------------------------------------------------------------------------------------------+
|                                                                                                             |
+-----------------+-------------------------------------------------------------------------------------------+
|                 | .. _router-resources-requests-memory:                                                     |
|                 |                                                                                           |
| **Key**         | `router.resources.requests.memory                                                         |
|                 | <operator.html#router-resources-requests-memory>`_                                        |
+-----------------+-------------------------------------------------------------------------------------------+
| **Value**       | string                                                                                    |
+-----------------+-------------------------------------------------------------------------------------------+
| **Example**     | ``256M``                                                                                  |
+-----------------+-------------------------------------------------------------------------------------------+
| **Description** | The `Kubernetes memory requests                                                           |
|                 | <https://kubernetes.io/docs/concepts/configuration/manage-compute-resources-container/    |
|                 | #resource-requests-and-limits-of-pod-and-container>`_ for MySQL Router container          |
+-----------------+-------------------------------------------------------------------------------------------+
|                                                                                                             |
+-----------------+-------------------------------------------------------------------------------------------+
|                 | .. _router-resources-limits-memory:                                                       |
|                 |                                                                                           |
| **Key**         | `router.resources.limits.memory                                                           |
|                 | <operator.html#router-resources-limits-memory>`_                                          |
+-----------------+-------------------------------------------------------------------------------------------+
| **Value**       | string                                                                                    |
+-----------------+-------------------------------------------------------------------------------------------+
| **Example**     | ``256M``                                                                                  |
+-----------------+-------------------------------------------------------------------------------------------+
| **Description** | `Kubernetes memory limits                                                                 |
|                 | <https://kubernetes.io/docs/concepts/configuration/manage-compute-resources-container/    |
|                 | #resource-requests-and-limits-of-pod-and-container>`_ for MySQL Router container          |
+-----------------+-------------------------------------------------------------------------------------------+
|                                                                                                             |
+-----------------+-------------------------------------------------------------------------------------------+
|                 | .. _router-expose-type:                                                                   |
|                 |                                                                                           |
| **Key**         | `router.expose.type <operator.html#router-expose-type>`_                                  |
+-----------------+-------------------------------------------------------------------------------------------+
| **Value Type**  | string                                                                                    |
+-----------------+-------------------------------------------------------------------------------------------+
| **Example**     | ``ClusterIP``                                                                             |
+-----------------+-------------------------------------------------------------------------------------------+
| **Description** | The `Kubernetes Service Type                                                              |
|                 | <https://kubernetes.io/docs/concepts/services-networking/service/                         |
|                 | #publishing-services-service-types>`_ used for for MySQL Router instances xposure         |
+-----------------+-------------------------------------------------------------------------------------------+


.. _operator.orchestrator-section:

`Orchestrator Section <operator.html#operator-orchestrator-section>`_
--------------------------------------------------------------------------------

The ``orchestrator`` section in the `deploy/cr.yaml <https://github.com/percona/percona-server-mysql-operator/blob/main/deploy/cr.yaml>`__ file contains
configuration options for the HAProxy service.

.. tabularcolumns:: |p{2cm}|p{13.6cm}|


+-----------------+-------------------------------------------------------------------------------------------+
|                 | .. _orchestrator-size:                                                                    |
|                 |                                                                                           |
| **Key**         | `orchestrator.size <operator.html#orchestrator-size>`_                                    |
+-----------------+-------------------------------------------------------------------------------------------+
| **Value**       | int                                                                                       |
+-----------------+-------------------------------------------------------------------------------------------+
| **Example**     | ``3``                                                                                     |
+-----------------+-------------------------------------------------------------------------------------------+
| **Description** | The number of the Orchestrator Pods to provide load balancing                             |
+-----------------+-------------------------------------------------------------------------------------------+
|                                                                                                             |
+-----------------+-------------------------------------------------------------------------------------------+
|                 | .. _orchestrator-image:                                                                   |
|                 |                                                                                           |
| **Key**         | `orchestrator.image <operator.html#orchestrator-image>`_                                  |
+-----------------+-------------------------------------------------------------------------------------------+
| **Value**       | string                                                                                    |
+-----------------+-------------------------------------------------------------------------------------------+
| **Example**     | ``perconalab/percona-server-mysql-operator:main-orchestrator``                            |
+-----------------+-------------------------------------------------------------------------------------------+
| **Description** | Orchestrator Docker image to use                                                          |
+-----------------+-------------------------------------------------------------------------------------------+
|                                                                                                             |
+-----------------+-------------------------------------------------------------------------------------------+
|                 | .. _orchestrator-imagepullpolicy:                                                         |
|                 |                                                                                           |
| **Key**         | `orchestrator.imagePullPolicy <operator.html#orchestrator-imagepullpolicy>`_              |
+-----------------+-------------------------------------------------------------------------------------------+
| **Value**       | string                                                                                    |
+-----------------+-------------------------------------------------------------------------------------------+
| **Example**     | ``Always``                                                                                |
+-----------------+-------------------------------------------------------------------------------------------+
| **Description** | The `policy used to update images <https://kubernetes.io/docs/concepts/containers/images/ |
|                 | #updating-images>`_                                                                       |
+-----------------+-------------------------------------------------------------------------------------------+
|                                                                                                             |
+-----------------+-------------------------------------------------------------------------------------------+
|                 | .. _orchestrator-affinity-antiaffinitytopologykey:                                        |
|                 |                                                                                           |
| **Key**         | `orchestrator.affinity.antiAffinityTopologyKey                                            |
|                 | <operator.html#orchestrator-affinity-antiAffinityTopologyKey>`_                           |
+-----------------+-------------------------------------------------------------------------------------------+
| **Value**       | string                                                                                    |
+-----------------+-------------------------------------------------------------------------------------------+
| **Example**     | ``kubernetes.io/hostname``                                                                |
+-----------------+-------------------------------------------------------------------------------------------+
| **Description** | The Operator `topology key                                                                |
|                 | <https://kubernetes.io/docs/concepts/configuration/assign-pod-node/                       |
|                 | #affinity-and-anti-affinity>`_ node anti-affinity constraint                              |
+-----------------+-------------------------------------------------------------------------------------------+
|                                                                                                             |
+-----------------+-------------------------------------------------------------------------------------------+
|                 | .. _orchestrator-affinity-advanced:                                                       |
|                 |                                                                                           |
| **Key**         | `orchestrator.affinity.advanced                                                           |
|                 | <operator.html#orchestrator-affinity-advanced>`_                                          |
+-----------------+-------------------------------------------------------------------------------------------+
| **Value**       | subdoc                                                                                    |
+-----------------+-------------------------------------------------------------------------------------------+
| **Example**     |                                                                                           |
+-----------------+-------------------------------------------------------------------------------------------+
| **Description** | In cases where the Pods require complex tuning the `advanced` option turns off the        |
|                 | ``topologyKey`` effect. This setting allows the standard Kubernetes affinity constraints  |
|                 | of any complexity to be used                                                              |
+-----------------+-------------------------------------------------------------------------------------------+
|                                                                                                             |
+-----------------+-------------------------------------------------------------------------------------------+
|                 | .. _orchestrator-resources-requests-memory:                                               |
|                 |                                                                                           |
| **Key**         | `orchestrator.resources.requests.memory                                                   |
|                 | <operator.html#orchestrator-resources-requests-memory>`_                                  |
+-----------------+-------------------------------------------------------------------------------------------+
| **Value**       | string                                                                                    |
+-----------------+-------------------------------------------------------------------------------------------+
| **Example**     | ``128M``                                                                                  |
+-----------------+-------------------------------------------------------------------------------------------+
| **Description** | The `Kubernetes memory requests                                                           |
|                 | <https://kubernetes.io/docs/concepts/configuration/manage-compute-resources-container/    |
|                 | #resource-requests-and-limits-of-pod-and-container>`_ for an Orchestrator container       |
+-----------------+-------------------------------------------------------------------------------------------+
|                                                                                                             |
+-----------------+-------------------------------------------------------------------------------------------+
|                 | .. _orchestrator-resources-limits-memory:                                                 |
|                 |                                                                                           |
| **Key**         | `orchestrator.resources.limits.memory                                                     |
|                 | <operator.html#orchestrator-resources-limits-memory>`_                                    |
+-----------------+-------------------------------------------------------------------------------------------+
| **Value**       | string                                                                                    |
+-----------------+-------------------------------------------------------------------------------------------+
| **Example**     | ``256M``                                                                                  |
+-----------------+-------------------------------------------------------------------------------------------+
| **Description** | `Kubernetes memory limits                                                                 |
|                 | <https://kubernetes.io/docs/concepts/configuration/manage-compute-resources-container/    |
|                 | #resource-requests-and-limits-of-pod-and-container>`_ for an Orchestrator container       |
+-----------------+-------------------------------------------------------------------------------------------+
|                                                                                                             |
+-----------------+-------------------------------------------------------------------------------------------+
|                 | .. _orchestrator-volumespec-persistentvolumeclaim-resources-requests-storage:             |
|                 |                                                                                           |
| **Key**         | `orchestrator.volumeSpec.persistentVolumeClaim.resources.requests.storage                 |
|                 | <operator.html#orchestrator-volumespec-persistentvolumeclaim-resources-requests-storage>`_|
+-----------------+-------------------------------------------------------------------------------------------+
| **Value**       | string                                                                                    |
+-----------------+-------------------------------------------------------------------------------------------+
| **Example**     | ``1Gi``                                                                                   |
+-----------------+-------------------------------------------------------------------------------------------+
| **Description** | The `Kubernetes PersistentVolumeClaim                                                     |
|                 | <https://kubernetes.io/docs/concepts/storage/persistent-volumes/#                         |
|                 | persistentvolumeclaims>`_ size for the Orchestrator                                       |
+-----------------+-------------------------------------------------------------------------------------------+

.. _operator.pmm-section:

`PMM Section <operator.html#operator-pmm-section>`_
--------------------------------------------------------------------------------

The ``pmm`` section in the `deploy/cr.yaml <https://github.com/percona/percona-server-mysql-operator/blob/main/deploy/cr.yaml>`__ file contains configuration
options for Percona Monitoring and Management.

.. tabularcolumns:: |p{2cm}|p{13.6cm}|

+-----------------+-------------------------------------------------------------------------------------------+
|                 | .. _pmm-enabled:                                                                          |
|                 |                                                                                           |
| **Key**         | `pmm.enabled <operator.html#pmm-enabled>`_                                                |
+-----------------+-------------------------------------------------------------------------------------------+
| **Value**       | boolean                                                                                   |
+-----------------+-------------------------------------------------------------------------------------------+
| **Example**     | ``false``                                                                                 |
+-----------------+-------------------------------------------------------------------------------------------+
| **Description** | Enables or disables `monitoring Percona Server for MySQL with PMM                         |
|                 | <https://www.percona.com/doc/percona-xtradb-cluster/5.7/manual/monitoring.html>`_         |
+-----------------+-------------------------------------------------------------------------------------------+
|                                                                                                             |
+-----------------+-------------------------------------------------------------------------------------------+
|                 | .. _pmm-image:                                                                            |
|                 |                                                                                           |
| **Key**         | `pmm.image <operator.html#pmm-image>`_                                                    |
+-----------------+-------------------------------------------------------------------------------------------+
| **Value**       | string                                                                                    |
+-----------------+-------------------------------------------------------------------------------------------+
| **Example**     | ``percona/pmm-client:{{{pmm2recommended}}}``                                                             |
+-----------------+-------------------------------------------------------------------------------------------+
| **Description** | PMM client Docker image to use                                                            |
+-----------------+-------------------------------------------------------------------------------------------+
|                                                                                                             |
+-----------------+-------------------------------------------------------------------------------------------+
|                 | .. _pmm-imagepullpolicy:                                                                  |
|                 |                                                                                           |
| **Key**         | `pmm.imagePullPolicy <operator.html#pmm-imagepullpolicy>`_                                |
+-----------------+-------------------------------------------------------------------------------------------+
| **Value**       | string                                                                                    |
+-----------------+-------------------------------------------------------------------------------------------+
| **Example**     | ``Always``                                                                                |
+-----------------+-------------------------------------------------------------------------------------------+
| **Description** | The `policy used to update images <https://kubernetes.io/docs/concepts/containers/images/ |
|                 | #updating-images>`_                                                                       |
+-----------------+-------------------------------------------------------------------------------------------+
|                                                                                                             |
+-----------------+-------------------------------------------------------------------------------------------+
|                 | .. _pmm-serverhost:                                                                       |
|                 |                                                                                           |
| **Key**         | `pmm.serverHost <operator.html#pmm-serverhost>`_                                          |
+-----------------+-------------------------------------------------------------------------------------------+
| **Value**       |  string                                                                                   |
+-----------------+-------------------------------------------------------------------------------------------+
| **Example**     |  ``monitoring-service``                                                                   |
+-----------------+-------------------------------------------------------------------------------------------+
| **Description** | Address of the PMM Server to collect data from the cluster                                |
+-----------------+-------------------------------------------------------------------------------------------+
|                                                                                                             |
+-----------------+-------------------------------------------------------------------------------------------+
|                 | .. _pmm-serveruser:                                                                       |
|                 |                                                                                           |
| **Key**         | `pmm.serverUser <operator.html#pmm-serveruser>`_                                          |
+-----------------+-------------------------------------------------------------------------------------------+
| **Value**       | string                                                                                    |
+-----------------+-------------------------------------------------------------------------------------------+
| **Example**     | ``admin``                                                                                 |
+-----------------+-------------------------------------------------------------------------------------------+
| **Description** | The `PMM Serve_User                                                                       |
|                 | <https://www.percona.com/doc/percona-monitoring-and-management/glossary.option.html>`_.   |
|                 | The PMM Server password should be configured using Secrets                                |
+-----------------+-------------------------------------------------------------------------------------------+

