# Contributing to the Operator

Percona welcomes and encourages community contributions to help improve [Percona Operator for MySQL](https://www.percona.com/doc/kubernetes-operator-for-mysql/index.html). The Operator automates the creation, modification, or deletion of items in your Percona Server for MySQL environment, and contains the necessary Kubernetes settings to maintain a consistent Percona Server for MySQL instance.

## Prerequisites

Before submitting code or documentation contributions, you should first complete the following prerequisites.

### 1. Sign the CLA

Before you can contribute, we kindly ask you to sign our [Contributor License Agreement](https://cla-assistant.percona.com/percona/percona-server-mysql-operator) (CLA). You can do this using your GitHub account and one click.

### 2. Code of Conduct

Please make sure to read and observe the [Contribution Policy](code-of-conduct.md).

## Submitting a pull request

Improvement and bugfix tasks for the Operator are tracked in [Jira](https://jira.percona.com/projects/K8SPS/issues). Although not mandatory, it is a good practice to examine already open Jira issues before submitting a pull request. For bigger contributions, we suggest creating a Jira issue first and discussing it with the engineering team and community before proposing any code changes.

Another good place to discuss Percona Operator for MySQL with developers and other community members is the [community forum](https://forums.percona.com/c/mysql-mariadb/percona-kubernetes-operator-for-mysql).

### 1. Contributing to the source tree

Contributions to the source tree should follow the workflow described below:

1. First, you need to [fork the repository on GitHub](https://docs.github.com/en/github/collaborating-with-issues-and-pull-requests/syncing-a-fork), clone your fork locally, and then [sync your local fork to upstream](https://docs.github.com/en/github/collaborating-with-issues-and-pull-requests/syncing-a-fork). After that, before starting to work on changes, make sure to always sync your fork with upstream. 
2. Create a branch for changes you are planning to make. If there is a Jira ticket related to your contribution, it is recommended to name your branch in the following way: `<Jira issue number>-<short description>`, where the issue number is something like `K8SPS-22`.

   Create the branch in your local repo as follows:

   ```
   git checkout -b K8SPS-22-fix-feature-X
   ```

   When your changes are ready, make a commit, mentioning the Jira issue in the commit message, if any:

   ```
   git add .
   git commit -m "K8SPS-22 fixed by ......"
   git push -u origin K8SPS-22-fix-feature X
   ```
3. Build the image and test your changes.

   The build is actually controlled by the `e2e-tests/build` script, and  we will invoke it through the traditional `make` command. You can build the Operator locally, but you need to deploy it to Kubernetes via some Docker registry to check how the modified version works (while installing the Custom Resource and generating necessary Pods for the Operator, image will be pulled from your remote repository). Therefore, building with the automatic image upload is the main scenario.
   
   Before doing this, make sure that you have your account created on [docker.io](https://www.docker.com/) and that you have logged-in from your terminal [docker login](https://docs.docker.com/engine/reference/commandline/login/).
   First we are going to create the custom image with `make` utility (the default one is [perconalab/percona-server-mysql-operator:<name-of-the-current-branch>](https://hub.docker.com/r/perconalab/percona-server-mysql-operator/) (`Makefile` will automatically detect the image tag from the current branch).
   By default our build script (`e2e-tests/build`) disables caching and uses the experimental squash feature of Docker. To disable this behavior and build the image use the following command:

   ```
   DOCKER_SQUASH=0 DOCKER_NOCACHE=0 make IMAGE=<your-docker-id>/<custom-repository-name>:<custom-tag> 
   ```

   Let's use `myid/percona-server-mysql-operator` as `your-docker-id` and `custom-repository-name`, and follow the previous example:

   ```
   DOCKER_SQUASH=0 DOCKER_NOCACHE=0 make IMAGE=myid/percona-server-mysql-operator:k8sps-22
   ```

   The process will build and push the image to your Docker Hub.
   
   If you just want to build the image without uploading, use environment variable `DOCKER_PUSH=0`. In this case, after the image is built you can push it manually:

   ```
   docker push <your-docker-id>/<custom-repository-name>:<custom-tag>
   ```

   If you don't want the image tag to be detected from the current branch, you can just override the image with:

   ```
   DOCKER_SQUASH=0 DOCKER_NOCACHE=0 make IMAGE_TAG_BASE=<your-docker-id>/percona-server-mysql-operator
   ```

   Once your image is built and ready, install Custom Resource Definitions (CRDs) and deploy the Operator to your cluster:

   ```
   make install deploy IMAGE=<your-docker-id>/percona-server-mysql-operator:k8sps-22
   ```

   If everything goes OK, that Deployment for the Operator, CRDs, Secret objects and ComfigMaps will be created:

   ```
   kubectl get all
   NAME                                                 READY   STATUS    RESTARTS   AGE
   pod/percona-server-mysql-operator-7cd85cfb7b-xxbsm   1/1     Running   0          49s

   NAME                                            READY   UP-TO-DATE   AVAILABLE   AGE
   deployment.apps/percona-server-mysql-operator   1/1     1            1           49s

   NAME                                                       DESIRED   CURRENT   READY   AGE
   replicaset.apps/percona-server-mysql-operator-7cd85cfb7b   1         1         1       49s

   kubectl get crds
   perconaservermysqlbackups.ps.percona.com    2022-06-27T15:14:49Z
   perconaservermysqlrestores.ps.percona.com   2022-06-27T15:14:49Z
   perconaservermysqls.ps.percona.com          2022-06-27T15:14:49Z
   ```

   To verify your Operator works correctly, you can check its logs:
   ```
   kubectl logs percona-server-mysql-operator-<pod-hash>
   ```
   To get more detailed information in the Operator log, change `LOG_LEVEL` value to `DEBUG`
   in the `deploy/operator.yaml`file.
   Now you need to create a custom resource (CR) from CRD.
   Here we will show CR for percona cluster with default asynchronous replication:
   ```
   kubectl apply -f config/samples/ps_v2_perconaserverformysql.yaml
   perconaservermysql.ps.percona.com/cluster1 created
   ```

   Check the CR and other resources:
   ```
   kubectl get ps
   NAME       REPLICATION   ENDPOINT                         STATE   MYSQL   ORCHESTRATOR   ROUTER   AGE
   cluster1   async         cluster1-mysql-primary.default   ready   3       3                       14m

   kubectl get all
   NAME                                                 READY   STATUS    RESTARTS      AGE
   pod/cluster1-mysql-0                                 2/2     Running   0             16m
   pod/cluster1-mysql-1                                 2/2     Running   1 (15m ago)   15m
   pod/cluster1-mysql-2                                 2/2     Running   1 (14m ago)   14m
   pod/cluster1-orc-0                                   2/2     Running   0             10m
   pod/cluster1-orc-1                                   2/2     Running   0             11m
   pod/cluster1-orc-2                                   2/2     Running   0             12m
   pod/percona-server-mysql-operator-6b65cdc6fc-nkzlk   1/1     Running   0             26m

   NAME                             TYPE        CLUSTER-IP       EXTERNAL-IP   PORT(S)                                 AGE
   service/cluster1-mysql           ClusterIP   None             <none>        3306/TCP,33062/TCP,33060/TCP,6033/TCP   16m
   service/cluster1-mysql-primary   ClusterIP   10.110.253.17    <none>        3306/TCP,33062/TCP,33060/TCP,6033/TCP   16m
   service/cluster1-mysql-unready   ClusterIP   None             <none>        3306/TCP,33062/TCP,33060/TCP,6033/TCP   16m
   service/cluster1-orc             ClusterIP   None             <none>        3000/TCP,10008/TCP                      16m
   service/cluster1-orc-0           ClusterIP   10.101.142.185   <none>        3000/TCP,10008/TCP                      16m
   service/cluster1-orc-1           ClusterIP   10.109.216.54    <none>        3000/TCP,10008/TCP                      16m
   service/cluster1-orc-2           ClusterIP   10.97.8.119      <none>        3000/TCP,10008/TCP                      16m

   NAME                                            READY   UP-TO-DATE   AVAILABLE   AGE
   deployment.apps/percona-server-mysql-operator   1/1     1            1           26m

   NAME                                                       DESIRED   CURRENT   READY   AGE
   replicaset.apps/percona-server-mysql-operator-6b65cdc6fc   1         1         1       26m

   NAME                              READY   AGE
   statefulset.apps/cluster1-mysql   3/3     16m
   statefulset.apps/cluster1-orc     3/3     16m
   ```

   To clean the Operator Deployment, Custom Resource Definitions as well as Custom Resource objects, run
   ```
   make undeploy uninstall
   ```
   For the next run don't forget to clean PVCs too.

4. Create a pull request to the main repository on GitHub.
5. When the reviewer makes some comments, address any feedback that comes and update the pull request.
6. When your contribution is accepted, your pull request will be approved and merged to the main branch.


### 2. Contributing to documentation

The workflow for documentation is similar. Please take into account a few things:

1. All documentation is written using the [Sphinx engine markup language](https://www.sphinx-doc.org/). Sphinx allows easy publishing of various output formats such as HTML, LaTeX (for PDF), ePub, Texinfo, etc.
2. We store the documentation as *.rst files in the [ps-docs](https://github.com/percona/percona-server-mysql-operator/tree/ps-docs) branch of the Operator GitHub repository. The documentation is licensed under the [Attribution 4.0 International license (CC BY 4.0)](https://creativecommons.org/licenses/by/4.0/).

After [installing Sphinx](https://www.sphinx-doc.org/en/master/usage/installation.html) you can use `make html` or `make latexpdf` commands having the documentation branch as your current directory to build HTML and PDF versions of the documentation respectively.

## Code review

### 1. Automated code review

Your pull request will go through an automated build and testing process, and you will have a comment with the report once all tests are over (usually, it takes about 1 hour).

### 2. Code review by the Operator developers

Your contribution will be reviewed by other developers contributing to the project. The more complex your changes are, the more experts will be involved. You will receive feedback and recommendations directly on your pull request on GitHub, so keep an eye on your submission and be prepared to make further amendments. The developers might even provide some concrete suggestions on how to modify your code to better match the project’s expectations.

