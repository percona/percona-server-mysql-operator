Binding Distribution for MySQL components to Specific Kubernetes Nodes
================================================================================

The Operator does good job automatically assigning new Pods to nodes
with sufficient to achieve balanced distribution across the cluster.
Still there are situations when it worth to ensure that pods will land
on specific nodes: for example, to get speed advantages of the SSD
equipped machine, or to reduce costs choosing nodes in a same
availability zone.

That's why ``mysql`` section of the `deploy/cr.yaml <https://raw.githubusercontent.com/percona/percona-server-mysql-operator/main/deploy/cr.yaml>`__
file contain keys which can be used to configure `node affinity <https://kubernetes.io/docs/concepts/configuration/assign-pod-node/#affinity-and-anti-affinity>`_.


Affinity and anti-affinity
--------------------------

Affinity makes Pod eligible (or not eligible - so called
“anti-affinity”) to be scheduled on the node which already has Pods with
specific labels. Particularly this approach is good to to reduce costs
making sure several Pods with intensive data exchange will occupy the
same availability zone or even the same node - or, on the contrary, to
make them land on different nodes or even different availability zones
for the high availability and balancing purposes.

Percona Distribution for MySQL Operator provides two approaches for doing this:

-  simple way to set anti-affinity for Pods, built-in into the Operator,
-  more advanced approach based on using standard Kubernetes
   constraints.

Simple approach - use topologyKey of the Percona Distribution for MySQL Operator
~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~

Percona Distribution for MySQL Operator provides the ``antiAffinityTopologyKey``
option, which may have one of the following values:

-  ``kubernetes.io/hostname`` - Pods will avoid residing within the same
   host,
-  ``failure-domain.beta.kubernetes.io/zone`` - Pods will avoid residing
   within the same zone,
-  ``failure-domain.beta.kubernetes.io/region`` - Pods will avoid
   residing within the same region,
-  ``none`` - no constraints are applied.

The following example forces Percona Server for MySQL Pods to avoid occupying
the same node:

.. code:: yaml

   affinity:
     antiAffinityTopologyKey: "kubernetes.io/hostname"

Advanced approach - use standard Kubernetes constraints
~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~

Previous way can be used with no special knowledge of the Kubernetes way
of assigning Pods to specific nodes. Still in some cases more complex
tuning may be needed. In this case ``advanced`` option placed in the
`deploy/cr.yaml <https://github.com/percona/percona-xtradb-cluster-operator/blob/master/deploy/cr.yaml>`__
file turns off the effect of the ``topologyKey`` and allows to use
standard Kubernetes affinity constraints of any complexity:

.. code:: yaml

   affinity:
      advanced:
        podAffinity:
          requiredDuringSchedulingIgnoredDuringExecution:
          - labelSelector:
              matchExpressions:
              - key: security
                operator: In
                values:
                - S1
            topologyKey: failure-domain.beta.kubernetes.io/zone
        podAntiAffinity:
          preferredDuringSchedulingIgnoredDuringExecution:
          - weight: 100
            podAffinityTerm:
              labelSelector:
                matchExpressions:
                - key: security
                  operator: In
                  values:
                  - S2
              topologyKey: kubernetes.io/hostname
        nodeAffinity:
          requiredDuringSchedulingIgnoredDuringExecution:
            nodeSelectorTerms:
            - matchExpressions:
              - key: kubernetes.io/e2e-az-name
                operator: In
                values:
                - e2e-az1
                - e2e-az2
          preferredDuringSchedulingIgnoredDuringExecution:
          - weight: 1
            preference:
              matchExpressions:
              - key: another-node-label-key
                operator: In
                values:
                - another-node-label-value

See explanation of the advanced affinity options `in Kubernetes
documentation <https://kubernetes.io/docs/concepts/configuration/assign-pod-node/#inter-pod-affinity-and-anti-affinity-beta-feature>`__.

