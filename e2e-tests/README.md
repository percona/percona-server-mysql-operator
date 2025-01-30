# Building and testing the Operator

## Requirements

You need to install a number of software packages on your system to satisfy the build dependencies for building the Operator and/or to run its automated tests:

* [kubectl](https://kubernetes.io/docs/tasks/tools/) - Kubernetes command-line tool
* [docker](https://www.docker.com/) - platform for developing, shipping, and running applications in containers
* [sed](https://www.gnu.org/software/sed/manual/sed.html) - CLI stream editor
* [helm](https://helm.sh/) - the package manager for Kubernetes
* [jq](https://stedolan.github.io/jq/) - command-line JSON processor
* [yq](https://github.com/mikefarah/yq) - command-line YAML processor
* [krew](https://github.com/kubernetes-sigs/krew) - package manager for kubectl plugins
* [assert](https://github.com/morningspace/kubeassert) kubectl plugin
* [kuttl](https://kuttl.dev/) Kubernetes test framework
* [gcloud](https://cloud.google.com/sdk/gcloud) - Google Cloud command-line tool (if the ability to run tests on Google Cloud is needed)
* [shfmt](https://github.com/mvdan/sh) - A shell parser, formatter, and interpreter

### CentOS

Run the following commands to install the required components:

```
sudo yum -y install epel-release https://repo.percona.com/yum/percona-release-latest.noarch.rpm
sudo yum -y install coreutils sed jq curl docker percona-xtrabackup-24
sudo curl -s -L https://github.com/mikefarah/yq/releases/download/4.35.1/yq_linux_amd64 -o /usr/bin/yq
sudo chmod a+x /usr/bin/yq
curl -s -L https://github.com/openshift/origin/releases/download/v3.11.0/openshift-origin-client-tools-v3.11.0-0cbc58b-linux-64bit.tar.gz \
    | tar -C /usr/bin --strip-components 1 --wildcards -zxvpf - '*/oc' '*/kubectl'
curl -s https://get.helm.sh/helm-v3.12.3-linux-amd64.tar.gz \
    | tar -C /usr/bin --strip-components 1 -zxvpf - '*/helm'
curl https://sdk.cloud.google.com | bash
curl -fsSL https://github.com/kubernetes-sigs/krew/releases/latest/download/krew-linux_amd64.tar.gz \
    | tar -xzf - 
./krew-linux_amd64 install krew
export PATH="\${KREW_ROOT:-\$HOME/.krew}/bin:\$PATH"
kubectl krew install assert
kubectl krew install --manifest-url https://raw.githubusercontent.com/kubernetes-sigs/krew-index/a67f31ecb2e62f15149ca66d096357050f07b77d/plugins/kuttl.yaml
sudo tee /etc/yum.repos.d/google-cloud-sdk.repo << EOF
[google-cloud-cli]
name=Google Cloud CLI
baseurl=https://packages.cloud.google.com/yum/repos/cloud-sdk-el7-x86_64
enabled=1
gpgcheck=1
repo_gpgcheck=0
gpgkey=https://packages.cloud.google.com/yum/doc/rpm-package-key.gpg
EOF
sudo yum install -y google-cloud-cli google-cloud-cli-gke-gcloud-auth-plugin
```

### Runtime requirements

Also, you need a Kubernetes platform of [supported version](https://docs.percona.com/percona-operator-for-mysql/ps/System-Requirements.html#supported-platforms), available via [EKS](https://docs.percona.com/percona-operator-for-mysql/ps/eks.html), [GKE](https://docs.percona.com/percona-operator-for-mysql/ps/gke.html), or [minikube](https://docs.percona.com/percona-operator-for-mysql/ps/minikube.html) to run the Operator.

**Note:** there is no need to build an image if you are going to test some already-released version.

## Building and testing the Operator

There are scripts which build the image and run tests. Both building and testing
needs some repository for the newly created docker images. If nothing is
specified, scripts use Percona's experimental repository `perconalab/percona-server-mysql-operator`, which
requires decent access rights to make a push.

To specify your own repository for the Operator docker image, you can use IMAGE environment variable:

```
export IMAGE=bob/my_repository_for_test_images:K8SPS-129-fix-feature-X
```
We use linux/amd64 platform by default. To specify another platform, you can use DOCKER_DEFAULT_PLATFORM environment variable

```
export DOCKER_DEFAULT_PLATFORM=linux/amd64
```

Use the following script to build the image:

```
./e2e-tests/build
```

Tests can be run one-by-one as follows:

```
kubectl kuttl test --config e2e-tests/kuttl.yaml --test "^test-name\$"
```

Test names can be found in subdirectories of `e2e-tests/tests` (their names should be self-explanatory):

```
./e2e-tests/tests/async-ignore-annotations
./e2e-tests/tests/gr-demand-backup
./e2e-tests/tests/gr-one-pod
./e2e-tests/tests/gr-users
./e2e-tests/tests/one-pod
./e2e-tests/tests/sidecars
./e2e-tests/tests/auto-config
./e2e-tests/tests/gr-demand-backup-haproxy
./e2e-tests/tests/gr-recreate
./e2e-tests/tests/haproxy
./e2e-tests/tests/operator-self-healing
./e2e-tests/tests/smart-update
./e2e-tests/tests/config
....
```

## Using environment variables to customize the testing process

### Re-declaring default image names

You can use environment variables to re-declare all default docker images used for testing. The
full list of variables is the following one:

* `IMAGE` - the Operator, `perconalab/percona-server-mysql-operator:main` by default,
* `IMAGE_MYSQL` - Percona Distribution for MySQL, `perconalab/percona-server:main` by default,
* `IMAGE_PMM_CLIENT` - Percona Monitoring and Management (PMM) client, `perconalab/pmm-client:dev-latest` by default,
* `IMAGE_PROXY` - ProxySQL, `perconalab/percona-xtradb-cluster-operator:main-proxysql` by default,
* `IMAGE_HAPROXY` - HA Proxy, `perconalab/haproxy:main` by default,
* `IMAGE_BACKUP` - backups, `perconalab/percona-xtrabackup:main` by default,
* `IMAGE_ORCHESTRATOR` - Orchestrator, `perconalab/percona-orchestrator:main` by default,
* `IMAGE_ROUTER` - Router, `perconalab/percona-mysql-router:main` by default,
* `IMAGE_TOOLKIT` - Percona Toolkit, `perconalab/percona-toolkit:main` by default,

Also, you can set `DEBUG_TESTS` environment variable to `1` for more verbose outout.

### Using automatic clean-up after testing

By default, each test creates its own namespace and does full clean up after it finishes.

You can avoid automatic deletion of such leftovers as follows:

```
kubectl kuttl test --config e2e-tests/kuttl.yaml --test "^test-name\$" --skip-delete
```
