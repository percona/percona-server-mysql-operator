.. _operator-scale:

Scale Percona Distribution for MySQL on Kubernetes
========================================================

One of the great advantages brought by Kubernetes 
platform is the ease of an application scaling. Scaling an application
results in adding or removing the Pods and scheduling them to available 
Kubernetes nodes.

Size of the cluster is controlled by a :ref:`size key<mysql-size>` in the :ref:`operator.custom-resource-options` configuration. That’s why scaling the cluster needs
nothing more but changing this option and applying the updated
configuration file. This may be done in a specifically saved config, or
on the fly, using the following command:

.. code:: bash

   $ kubectl patch mysql cluster1 --type='json' -p='[{"op": "replace", "path": "/spec/mysql/size", "value": 5 }]'


In this example we have changed the size of the Percona Server for MySQL
Cluster to ``5`` instances.

Increase the Persistent Volume Claim size
-----------------------------------------

Kubernetes manages storage with a PersistentVolume (PV), a segment of
storage supplied by the administrator, and a PersistentVolumeClaim
(PVC), a request for storage from a user. In Kubernetes v1.11 the
feature was added to allow a user to increase the size of an existing
PVC object. The user cannot shrink the size of an existing PVC object.
Certain volume types support, by default, expanding PVCs (details about
PVCs and the supported volume types can be found in `Kubernetes
documentation <https://kubernetes.io/docs/concepts/storage/persistent-volumes/#expanding-persistent-volumes-claims>`__)

The following are the steps to increase the size:

#. Extract and backup the yaml file for the cluster

   .. code:: bash

      kubectl get ps cluster1 -o yaml --export > CR_backup.yaml

#. Now you should delete the cluster.

   ..
      UNCOMMENT THIS WHEN FINALIZERS GET WORKING
      warining Make sure that :ref:`delete-pxc-pvc<finalizers-pxc>` finalizer
      is not set in your custom resource, **otherwise
      all cluster data will be lost!**

   You can use the following command to delete the cluster:

   .. code:: bash

      kubectl delete -f CR_backup.yaml

#. For each node, edit the yaml to resize the PVC object.

   .. code:: bash

      kubectl edit pvc datadir-cluster1-mysql-0

   In the yaml, edit the spec.resources.requests.storage value.

   .. code:: bash

      spec:
        accessModes:
        - ReadWriteOnce
        resources:
          requests:
            storage: 6Gi

   Perform the same operation on the other nodes.

   .. code:: bash

      kubectl edit pvc datadir-cluster1-mysql-1
      kubectl edit pvc datadir-cluster1-mysql-2

#. In the CR configuration file, use vim or another text editor to edit
   the PVC size.

   .. code:: bash

      vim CR_backup.yaml

#. Apply the updated configuration to the cluster.

   .. code:: bash

      kubectl apply -f CR_backup.yaml
