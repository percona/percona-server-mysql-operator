package psclusterset

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var _ = Describe("PerconaServerMySQLClusterSet CEL Validations", Ordered, func() {
	ctx := context.Background()

	const ns = "psclusterset-validations"
	namespace := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name:      ns,
			Namespace: ns,
		},
	}

	// validClusterSet returns a fully valid ClusterSet that satisfies every
	// schema and CEL validation. Each test starts from this baseline and mutates
	// a single field to exercise one rule in isolation.
	validClusterSet := func(name string) *apiv1.PerconaServerMySQLClusterSet {
		return &apiv1.PerconaServerMySQLClusterSet{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: ns,
			},
			Spec: apiv1.PerconaServerMySQLClusterSetSpec{
				PrimaryCluster: "dc1",
				CredentialsSecret: corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: "clusterset-credentials",
					},
					Key: "credentials",
				},
				MySQLShellRunner: apiv1.MySQLShellRunnerSpec{
					Image: "percona/percona-mysql-shell:latest",
				},
				CreateReplicaClusterOptions: apiv1.CreateReplicaClusterOptions{
					RecoveryMethod: apiv1.RecoveryMethodClone,
				},
				Clusters: []apiv1.ClusterSetCluster{
					{
						InnoDBClusterName: "dc1",
						Endpoints: []apiv1.ClusterSetClusterEndpoint{
							{Host: "dc1-mysql-primary.test-clusterset.svc.cluster.local"},
						},
					},
					{
						InnoDBClusterName: "dc2",
						Endpoints: []apiv1.ClusterSetClusterEndpoint{
							{Host: "dc2-mysql-primary.test-clusterset.svc.cluster.local"},
						},
					},
				},
			},
		}
	}

	BeforeAll(func() {
		By("Creating the Namespace to perform the tests")
		err := k8sClient.Create(ctx, namespace)
		Expect(err).To(Not(HaveOccurred()))
	})

	AfterAll(func() {
		By("Deleting the Namespace to perform the tests")
		_ = k8sClient.Delete(ctx, namespace)
	})

	Context("Sanity check", func() {
		It("Should accept a fully valid ClusterSet", func() {
			pcs := validClusterSet("valid-baseline")
			Expect(k8sClient.Create(ctx, pcs)).To(Succeed())
		})
	})

	Context("Check '.spec.primaryCluster' is a member of '.spec.clusters'", func() {
		It("Should reject a primaryCluster that is not in the clusters list", func() {
			pcs := validClusterSet("primary-not-member")
			pcs.Spec.PrimaryCluster = "dc3"
			err := k8sClient.Create(ctx, pcs)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("spec.primaryCluster not found in spec.clusters"))
		})

		It("Should accept a primaryCluster that points to the first cluster", func() {
			pcs := validClusterSet("primary-first")
			pcs.Spec.PrimaryCluster = "dc1"
			Expect(k8sClient.Create(ctx, pcs)).To(Succeed())
		})

		It("Should accept a primaryCluster that points to a non-first cluster", func() {
			pcs := validClusterSet("primary-second")
			pcs.Spec.PrimaryCluster = "dc2"
			Expect(k8sClient.Create(ctx, pcs)).To(Succeed())
		})
	})

	Context("Check '.spec.primaryCluster' is alphanumeric", func() {
		It("Should reject a primaryCluster containing a hyphen", func() {
			pcs := validClusterSet("primary-hyphen")
			pcs.Spec.PrimaryCluster = "dc-1"
			err := k8sClient.Create(ctx, pcs)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("primaryCluster must contain only alphanumeric characters"))
		})

		It("Should reject a primaryCluster containing a dot", func() {
			pcs := validClusterSet("primary-dot")
			pcs.Spec.PrimaryCluster = "dc.1"
			err := k8sClient.Create(ctx, pcs)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("primaryCluster must contain only alphanumeric characters"))
		})

		It("Should reject an empty primaryCluster", func() {
			pcs := validClusterSet("primary-empty")
			pcs.Spec.PrimaryCluster = ""
			err := k8sClient.Create(ctx, pcs)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("primaryCluster must contain only alphanumeric characters"))
		})

		It("Should accept a mixed-case alphanumeric primaryCluster", func() {
			pcs := validClusterSet("primary-mixedcase")
			pcs.Spec.PrimaryCluster = "DC1"
			pcs.Spec.Clusters[0].InnoDBClusterName = "DC1"
			Expect(k8sClient.Create(ctx, pcs)).To(Succeed())
		})
	})

	Context("Check '.spec.credentialsSecret.name' is set", func() {
		It("Should reject an empty credentialsSecret.name", func() {
			pcs := validClusterSet("creds-empty-name")
			pcs.Spec.CredentialsSecret.Name = ""
			err := k8sClient.Create(ctx, pcs)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("credentialsSecret.name must be set"))
		})

		It("Should accept a non-empty credentialsSecret.name", func() {
			pcs := validClusterSet("creds-with-name")
			pcs.Spec.CredentialsSecret.Name = "my-secret"
			Expect(k8sClient.Create(ctx, pcs)).To(Succeed())
		})
	})

	Context("Check '.spec.clusters[].innodbClusterName' is alphanumeric", func() {
		It("Should reject an innodbClusterName containing a hyphen", func() {
			pcs := validClusterSet("innodb-hyphen")
			pcs.Spec.PrimaryCluster = "dc2"
			pcs.Spec.Clusters[0].InnoDBClusterName = "dc-1"
			err := k8sClient.Create(ctx, pcs)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("innodbClusterName must contain only alphanumeric characters"))
		})

		It("Should reject an innodbClusterName containing an underscore", func() {
			pcs := validClusterSet("innodb-underscore")
			pcs.Spec.PrimaryCluster = "dc2"
			pcs.Spec.Clusters[0].InnoDBClusterName = "dc_1"
			err := k8sClient.Create(ctx, pcs)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("innodbClusterName must contain only alphanumeric characters"))
		})
	})

	Context("Check '.spec.clusters' names are unique", func() {
		It("Should reject duplicate innodbClusterName values", func() {
			pcs := validClusterSet("clusters-duplicate-name")
			pcs.Spec.Clusters[1].InnoDBClusterName = "dc1"
			err := k8sClient.Create(ctx, pcs)
			Expect(err).To(HaveOccurred())
		})

		It("Should accept distinct innodbClusterName values", func() {
			pcs := validClusterSet("clusters-unique-names")
			Expect(k8sClient.Create(ctx, pcs)).To(Succeed())
		})
	})

	Context("Check endpoint hosts are unique across all clusters", func() {
		It("Should reject the same host used in two different clusters", func() {
			pcs := validClusterSet("hosts-dup-across-clusters")
			pcs.Spec.Clusters[1].Endpoints[0].Host = pcs.Spec.Clusters[0].Endpoints[0].Host
			err := k8sClient.Create(ctx, pcs)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("endpoint host must be unique across all clusters"))
		})

		It("Should reject a host duplicated within a single cluster", func() {
			pcs := validClusterSet("hosts-dup-within-cluster")
			pcs.Spec.Clusters[0].Endpoints = append(pcs.Spec.Clusters[0].Endpoints,
				apiv1.ClusterSetClusterEndpoint{Host: pcs.Spec.Clusters[0].Endpoints[0].Host})
			err := k8sClient.Create(ctx, pcs)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("endpoint host must be unique across all clusters"))
		})

		It("Should accept multiple distinct hosts per cluster", func() {
			pcs := validClusterSet("hosts-multiple-distinct")
			pcs.Spec.Clusters[0].Endpoints = append(pcs.Spec.Clusters[0].Endpoints,
				apiv1.ClusterSetClusterEndpoint{Host: "dc1-mysql-replica.test-clusterset.svc.cluster.local"})
			Expect(k8sClient.Create(ctx, pcs)).To(Succeed())
		})
	})

	Context("Check '.spec.clusters[].endpoints[].host' is a valid IP or domain name", func() {
		It("Should accept a valid DNS subdomain host", func() {
			pcs := validClusterSet("host-valid-dns")
			pcs.Spec.Clusters[0].Endpoints[0].Host = "mysql.example.com"
			Expect(k8sClient.Create(ctx, pcs)).To(Succeed())
		})

		It("Should accept a valid IPv4 address as host", func() {
			pcs := validClusterSet("host-valid-ipv4")
			pcs.Spec.Clusters[0].Endpoints[0].Host = "10.0.0.1"
			Expect(k8sClient.Create(ctx, pcs)).To(Succeed())
		})

		It("Should accept a valid IPv6 address as host", func() {
			pcs := validClusterSet("host-valid-ipv6")
			pcs.Spec.Clusters[0].Endpoints[0].Host = "2001:db8::1"
			Expect(k8sClient.Create(ctx, pcs)).To(Succeed())
		})

		It("Should reject a host containing uppercase characters", func() {
			pcs := validClusterSet("host-uppercase")
			pcs.Spec.Clusters[0].Endpoints[0].Host = "Invalid.Example.Com"
			err := k8sClient.Create(ctx, pcs)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("host must be a valid IP address or domain name"))
		})

		It("Should reject a host containing an underscore", func() {
			pcs := validClusterSet("host-underscore")
			pcs.Spec.Clusters[0].Endpoints[0].Host = "my_host.example.com"
			err := k8sClient.Create(ctx, pcs)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("host must be a valid IP address or domain name"))
		})

		It("Should reject a host containing whitespace", func() {
			pcs := validClusterSet("host-whitespace")
			pcs.Spec.Clusters[0].Endpoints[0].Host = "not a host"
			err := k8sClient.Create(ctx, pcs)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("host must be a valid IP address or domain name"))
		})
	})
})
