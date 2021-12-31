.. _tls:

Transport Layer Security (TLS)
******************************

The Percona Distribution for MySQL Operator uses Transport Layer
Security (TLS) cryptographic protocol for the following types of communication:

* Internal - communication between Percona Server for MySQL instances,
* External - communication between the client application and ProxySQL.

The internal certificate is also used as an authorization method.

TLS security can be configured in several ways. By default, the Operator
generates long-term certificates automatically if there are no certificate
secrets available. But certificates can be generated manually as well.

.. contents:: :local:

.. _tls.certs.manual:

Generate certificates manually
==============================

To generate certificates manually, follow these steps:

1. Provision a Certificate Authority (CA) to generate TLS certificates

2. Generate a CA key and certificate file with the server details

3. Create the server TLS certificates using the CA keys, certs, and server
   details

The set of commands generate certificates with the following attributes:

*  ``Server-pem`` - Certificate

*  ``Server-key.pem`` - the private key

*  ``ca.pem`` - Certificate Authority

You should generate certificates twice: one set is for external communications,
and another set is for internal ones. A secret created for the external use must
be added to ``cr.yaml/spec/secretsName``. A certificate generated for internal
communications must be added to the ``cr.yaml/spec/sslInternalSecretName``.

.. code:: bash

   $ cat <<EOF | cfssl gencert -initca - | cfssljson -bare ca
   {
     "CN": "Root CA",
     "key": {
       "algo": "rsa",
       "size": 2048
     }
   }
   EOF

   $ cat <<EOF | cfssl gencert -ca=ca.pem  -ca-key=ca-key.pem - | cfssljson -bare server
   {
     "hosts": [
       "${CLUSTER_NAME}-proxysql",
       "*.${CLUSTER_NAME}-proxysql-unready",
       "*.${CLUSTER_NAME}-pxc"
     ],
     "CN": "${CLUSTER_NAME}-pxc",
     "key": {
       "algo": "rsa",
       "size": 2048
     }
   }
   EOF

   $ kubectl create secret generic my-cluster-ssl --from-file=tls.crt=server.pem --
   from-file=tls.key=server-key.pem --from-file=ca.crt=ca.pem --
   type=kubernetes.io/tls

.. _tls.certs.update:

Update certificates
===================

.. _tls.certs.update.check:

Check your certificates for expiration
--------------------------------------

#. First, check the necessary secrets names (``cluster1-ssl`` and 
   ``cluster1-ssl-internal`` by default):

   .. code:: bash

      $ kubectl get certificate

   You will have the following response:

   .. code:: text

      NAME                    READY   SECRET                    AGE
      cluster1-ssl            True    cluster1-ssl            49m
      cluster1-ssl-internal   True    cluster1-ssl-internal   49m

#. Optionally you can also check that the certificates issuer is up and running:

   .. code:: bash

      $ kubectl get issuer

   The response should be as follows:

   .. code:: text

      NAME                READY   AGE
      cluster1-mysql-ca   True    49m

#. Now use the following command to find out the certificates validity dates,
   substituting Secrets names if necessary:

   .. code:: bash

      $ {
        kubectl get secret/cluster1-ssl-internal -o jsonpath='{.data.tls\.crt}' | base64 --decode | openssl x509 -inform pem -noout -text | grep "Not After"
        kubectl get secret/cluster1-ssl -o jsonpath='{.data.ca\.crt}' | base64 --decode | openssl x509 -inform pem -noout -text | grep "Not After"
        }

   The resulting output will be self-explanatory:

   .. code:: text

      Not After : Sep 15 11:04:53 2021 GMT
      Not After : Sep 15 11:04:53 2021 GMT

.. _tls.certs.update.without.downtime:

Update certificates without downtime
------------------------------------

If you have created certificates manually, you can follow the next steps to
perform a no-downtime update of these certificates *if they are still valid*.

.. note:: For already expired certificates, follow :ref:`the alternative way<tls.certs.update.with.downtime>`.

Having non-expired certificates, you can roll out new certificates (both CA and TLS) with the Operator
as follows.

#. Generate a new CA certificate (``ca.pem``). Optionally you can also generate
   a new TLS certificate and a key for it, but those can be generated later on
   step 6.

#. Get the current CA (``ca.pem.old``) and TLS (``tls.pem.old``) certificates
   and the TLS certificate key (``tls.key.old``):

   .. code:: bash

      $ kubectl get secret/cluster1-ssl-internal -o jsonpath='{.data.ca\.crt}' | base64 --decode > ca.pem.old
      $ kubectl get secret/cluster1-ssl-internal -o jsonpath='{.data.tls\.crt}' | base64 --decode > tls.pem.old
      $ kubectl get secret/cluster1-ssl-internal -o jsonpath='{.data.tls\.key}' | base64 --decode > tls.key.old

#. Combine new and current ``ca.pem`` into a ``ca.pem.combined`` file:

   .. code:: bash

      $ cat ca.pem ca.pem.old >> ca.pem.combined
 
#. Create a new Secrets object with *old* TLS certificate (``tls.pem.old``)
   and key (``tls.key.old``), but a *new combined* ``ca.pem``
   (``ca.pem.combined``):

   .. code:: bash

      $ kubectl delete secret/cluster1-ssl-internal
      $ kubectl create secret generic cluster1-ssl-internal --from-file=tls.crt=tls.pem.old --from-file=tls.key=tls.key.old --from-file=ca.crt=ca.pem.combined --type=kubernetes.io/tls

#. The cluster will go through a rolling reconciliation, but it will do it
   without problems, as every node has old TLS certificate/key, and both new
   and old CA certificates.

#. If new TLS certificate and key weren't generated on step 1,
   :ref:`do that <tls.certs.manual>` now.

#. Create a new Secrets object for the second time: use new TLS certificate
   (``server.pem`` in the example) and its key (``server-key.pem``), and again
   the combined CA certificate (``ca.pem.combined``):

   .. code:: bash

      $ kubectl delete secret/cluster1-ssl-internal
      $ kubectl create secret generic cluster1-ssl-internal --from-file=tls.crt=server.pem --from-file=tls.key=server-key.pem --from-file=ca.crt=ca.pem.combined --type=kubernetes.io/tls

#. The cluster will go through a rolling reconciliation, but it will do it
   without problems, as every node already has a new CA certificate (as a part
   of the combined CA certificate), and can successfully allow joiners with new
   TLS certificate to join. Joiner node also has a combined CA certificate, so
   it can authenticate against older TLS certificate.

#. Create a final Secrets object: use new TLS certificate (``server.pmm``) and
   its key (``server-key.pem``), and just the new CA certificate (``ca.pem``):

   .. code:: bash

      $ kubectl delete secret/cluster1-ssl-internal
      $ kubectl create secret generic cluster1-ssl-internal --from-file=tls.crt=server.pem --from-file=tls.key=server-key.pem --from-file=ca.crt=ca.pem --type=kubernetes.io/tls

#. The cluster will go through a rolling reconciliation, but it will do it
   without problems: the old CA certificate is removed, and every node is
   already using new TLS certificate and no nodes rely on the old CA
   certificate any more.

.. _tls.certs.update.with.downtime:

Update certificates with downtime
---------------------------------

If your certificates have been already expired, you should move through the
*pause - update Secrets - unpause* route as follows.

#. Pause the cluster :ref:`in a standard way<operator-pause>`, and make
   sure it has reached its paused state.

#. Delete Secrets to force the SSL reconciliation:

   .. code:: bash

      $ kubectl delete secret/cluster1-ssl secret/my-cluster-ssl-internal

#. :ref:`Check certificates<tls.certs.update.check>` to make sure reconciliation
   have succeeded.

#. Unpause the cluster :ref:`in a standard way<operator-pause>`, and make
   sure it has reached its running state.
