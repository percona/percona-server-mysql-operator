.. _tls:

Transport Layer Security (TLS)
******************************

The Percona Operator for MySQL uses Transport Layer
Security (TLS) cryptographic protocol for the following types of communication:

* Internal - communication between Percona Server for MySQL instances,
* External - communication between the client application and ProxySQL.

The internal certificate is also used as an authorization method.

TLS security can be configured in several ways. By default, the Operator
generates long-term certificates automatically if there are no certificate
secrets available. But certificates can be generated manually as well.

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


