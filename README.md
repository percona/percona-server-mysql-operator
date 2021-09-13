# Percona Server Operator

## Build

```
$ make docker-build IMG=perconalab/percona-server-operator:latest
```

## Deploy operator

```
$ kubectl apply -k config/crd
$ kubectl apply -k config/rbac
$ kubectl apply -k config/manager
```
