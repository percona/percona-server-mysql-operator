.. _install-minikube:

Install Percona Server for MySQL on Minikube
============================================

Installing the Percona Kubernetes Operator for Percona Server for MySQL on `minikube <https://github.com/kubernetes/minikube>`_
is the easiest way to try it locally without a cloud provider. Minikube runs
Kubernetes on GNU/Linux, Windows, or macOS system using a system-wide
hypervisor, such as VirtualBox, KVM/QEMU, VMware Fusion or Hyper-V. Using it is
a popular way to test the Kubernetes application locally prior to deploying it
on a cloud.

The following steps are needed to run Percona Kubernetes Operator for Percona
Server for MySQL on Minikube:

#. `Install Minikube <https://kubernetes.io/docs/tasks/tools/install-minikube/>`_,
   using a way recommended for your system. This includes the installation of
   the following three components:

   #. kubectl tool,
   #. a hypervisor, if it is not already installed,
   #. actual Minikube package

   After the installation, run ``minikube start --memory=4096 --cpus=3``
   (parameters increase the virtual machine limits for the CPU cores and memory,
   to ensure stable work of the Operator). Being executed, this command will
   download needed virtualized images, then initialize and run the
   cluster. After Minikube is successfully started, you can optionally run the
   Kubernetes dashboard, which visually represents the state of your cluster.
   Executing ``minikube dashboard`` will start the dashboard and open it in your
   default web browser.

#. Deploy the operator with the following command:

   .. code:: bash

      $ kubectl apply -f https://raw.githubusercontent.com/percona/percona-server-mysql-operator/main/deploy/bundle.yaml

#. Deploy Percona Server for MySQL:

   .. code:: bash

      $ kubectl apply -f https://raw.githubusercontent.com/percona/percona-server-mysql-operator/main/deploy/cr.yaml

   This deploys three Percona Server for MySQL instances and one Orchestrator
   instance. For more configuration options please see ``deploy/cr.yaml`` and
   :ref:`Custom Resource Options<operator.custom-resource-options>`.

   Creation process will take some time. The process is over when both
   operator and replica set pod have reached their Running status.
   ``kubectl get pods`` output should look like this:
   
   .. include:: ./assets/code/kubectl-get-minimal-response.txt
   
   You can clone the repository with all manifests and source code by executing the following command:

   .. code:: bash

      $ git clone -b v{{{release}}} https://github.com/percona/percona-server-mysql-operator

#. During previous steps, the Operator has generated several `secrets <https://kubernetes.io/docs/concepts/configuration/secret/>`_, including the
   password for the ``root`` user, which you will definitely need to access the
   cluster. Use ``kubectl get secrets`` to see the list of Secrets objects (by
   default Secrets object you are interested in has ``cluster1-secrets`` name).
   Then ``kubectl get secret cluster1-secrets -o yaml`` will return the YAML
   file with generated secrets, including the root password which should look as
   follows::

     ...
     data:
       ...
       root: cmJ4MG9xdXpnb1pEZElqRHdX

   Here the actual password is base64-encoded, and
   ``echo 'cmJ4MG9xdXpnb1pEZElqRHdX' | base64 --decode`` will bring it back to a
   human-readable form.

#. Check connectivity to a newly created cluster.

   First of all, run percona-client and connect its console output to your
   terminal (running it may require some time to deploy the correspondent Pod): 
   
   .. code:: bash

      kubectl run -i --rm --tty percona-client --image=percona:8.0 --restart=Never -- bash -il
   
   Now run ``mysql`` tool in the percona-client command shell using the password
   obtained from the secret:
   
   .. code:: bash

      mysql -h minimal-cluster-haproxy -uroot -proot_password

   This command will connect you to the MySQL monitor.

   .. include:: ./assets/code/mysql-welcome-response.txt

