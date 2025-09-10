package ps

import (
	"context"
	"os"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

var _ = Describe("Reconcile telemetry sending", Ordered, func() {
	ctx := context.Background()

	const crName = "telemetry-test"
	const ns = crName

	namespace := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name:      ns,
			Namespace: ns,
		},
	}

	BeforeAll(func() {
		By("Creating the Namespace to perform the tests")
		err := k8sClient.Create(ctx, namespace)
		Expect(err).To(Not(HaveOccurred()))

		DeferCleanup(func() {
			err := os.Unsetenv("TELEMETRY_SERVICE_URL")
			Expect(err).NotTo(HaveOccurred())
		})
		// configuring a dummy URL to prevent Ginkgo tests from making API calls to the real one.
		err = os.Setenv("TELEMETRY_SERVICE_URL", "https://telemetry-dummy.percona.com")
		Expect(err).NotTo(HaveOccurred())
	})

	AfterAll(func() {
		By("Deleting the Namespace to perform the tests")
		_ = k8sClient.Delete(ctx, namespace)
	})

	cr, err := readDefaultCR(crName, ns)
	It("should read default cr.yaml", func() {
		Expect(err).NotTo(HaveOccurred())
	})

	It("should create PerconaServerMySQL", func() {
		Expect(k8sClient.Create(ctx, cr)).Should(Succeed())
		cr.Status.State = apiv1.StateReady

	})

	Context("When telemetry is disabled", func() {
		It("should not create telemetry job when telemetry is disabled", func() {
			DeferCleanup(func() {
				err := os.Unsetenv("DISABLE_TELEMETRY")
				Expect(err).NotTo(HaveOccurred())
			})
			err := os.Setenv("DISABLE_TELEMETRY", "true")
			Expect(err).NotTo(HaveOccurred())

			r := reconciler()

			err = r.reconcileScheduledTelemetrySending(ctx, cr)
			Expect(err).NotTo(HaveOccurred())

			jobName := telemetryJobName(cr)
			_, exists := r.Crons.telemetryJobs.Load(jobName)
			Expect(exists).To(BeFalse())

			Expect(r.Crons.crons.Entries()).To(HaveLen(0))
		})

		It("should remove existing telemetry job when telemetry is disabled", func() {
			r := reconciler()
			jobName := telemetryJobName(cr)

			// reconcile 1st time to create fully a job
			err := r.reconcileScheduledTelemetrySending(ctx, cr)
			Expect(err).NotTo(HaveOccurred())

			_, exists := r.Crons.telemetryJobs.Load(jobName)
			Expect(exists).To(BeTrue())

			Expect(r.Crons.crons.Entries()).To(HaveLen(1))

			DeferCleanup(func() {
				err := os.Unsetenv("DISABLE_TELEMETRY")
				Expect(err).NotTo(HaveOccurred())
			})
			err = os.Setenv("DISABLE_TELEMETRY", "true")
			Expect(err).NotTo(HaveOccurred())

			// reconcile 2nd time to remove the job
			err = r.reconcileScheduledTelemetrySending(ctx, cr)
			Expect(err).NotTo(HaveOccurred())

			_, exists = r.Crons.telemetryJobs.Load(jobName)
			Expect(exists).To(BeFalse())

			Expect(r.Crons.crons.Entries()).To(HaveLen(0))

		})
	})

	Context("When telemetry is enabled", func() {
		BeforeEach(func() {
			DeferCleanup(func() {
				err := os.Unsetenv("DISABLE_TELEMETRY")
				Expect(err).NotTo(HaveOccurred())
			})
			err := os.Setenv("DISABLE_TELEMETRY", "false")
			Expect(err).NotTo(HaveOccurred())
		})

		It("should create telemetry job when no existing job", func() {
			r := reconciler()

			err := r.reconcileScheduledTelemetrySending(ctx, cr)
			Expect(err).NotTo(HaveOccurred())

			jobName := telemetryJobName(cr)
			job, exists := r.Crons.telemetryJobs.Load(jobName)
			Expect(exists).To(BeTrue())

			telemetryJobVal, ok := job.(telemetryJob)
			Expect(ok).To(BeTrue())
			Expect(telemetryJobVal.cronSchedule).NotTo(BeEmpty())

			Expect(r.Crons.crons.Entries()).To(HaveLen(1))

		})

		It("should keep telemetry job after the 2nd reconciliation without configured schedule", func() {
			r := reconciler()
			jobName := telemetryJobName(cr)

			err := r.reconcileScheduledTelemetrySending(ctx, cr)
			Expect(err).NotTo(HaveOccurred())

			firstJob, firstExists := r.Crons.telemetryJobs.Load(jobName)
			Expect(firstExists).To(BeTrue())

			firstTelemetryJobVal, ok := firstJob.(telemetryJob)
			Expect(ok).To(BeTrue())

			Expect(r.Crons.crons.Entries()).To(HaveLen(1))

			err = r.reconcileScheduledTelemetrySending(ctx, cr)
			Expect(err).NotTo(HaveOccurred())

			secondJob, secondExists := r.Crons.telemetryJobs.Load(jobName)
			Expect(secondExists).To(BeTrue())

			secondTelemetryJobVal, ok := secondJob.(telemetryJob)
			Expect(ok).To(BeTrue())

			Expect(r.Crons.crons.Entries()).To(HaveLen(1))

			Expect(firstTelemetryJobVal.cronSchedule).To(Equal(secondTelemetryJobVal.cronSchedule))
		})

		It("should keep existing job with same schedule", func() {
			r := reconciler()
			jobName := telemetryJobName(cr)

			DeferCleanup(func() {
				err := os.Unsetenv("TELEMETRY_SCHEDULE")
				Expect(err).NotTo(HaveOccurred())
			})
			err := os.Setenv("TELEMETRY_SCHEDULE", "0 0 * * *")
			Expect(err).NotTo(HaveOccurred())

			err = r.reconcileScheduledTelemetrySending(ctx, cr)
			Expect(err).NotTo(HaveOccurred())

			firstJob, firstExists := r.Crons.telemetryJobs.Load(jobName)
			Expect(firstExists).To(BeTrue())

			firstTelemetryJobVal, ok := firstJob.(telemetryJob)
			Expect(ok).To(BeTrue())

			Expect(r.Crons.crons.Entries()).To(HaveLen(1))

			err = r.reconcileScheduledTelemetrySending(ctx, cr)
			Expect(err).NotTo(HaveOccurred())

			secondJob, secondExists := r.Crons.telemetryJobs.Load(jobName)
			Expect(secondExists).To(BeTrue())

			secondTelemetryJobVal, ok := secondJob.(telemetryJob)
			Expect(ok).To(BeTrue())

			Expect(r.Crons.crons.Entries()).To(HaveLen(1))

			Expect(firstTelemetryJobVal.cronSchedule).To(Equal(secondTelemetryJobVal.cronSchedule))
		})

		It("should replace existing job with different schedule", func() {
			r := reconciler()
			jobName := telemetryJobName(cr)

			DeferCleanup(func() {
				err := os.Unsetenv("TELEMETRY_SCHEDULE")
				Expect(err).NotTo(HaveOccurred())
			})
			err := os.Setenv("TELEMETRY_SCHEDULE", "1 2 3 * *")
			Expect(err).NotTo(HaveOccurred())

			err = r.reconcileScheduledTelemetrySending(ctx, cr)
			Expect(err).NotTo(HaveOccurred())

			job, exists := r.Crons.telemetryJobs.Load(jobName)
			Expect(exists).To(BeTrue())

			telJob, ok := job.(telemetryJob)
			Expect(ok).To(BeTrue())
			Expect(telJob.cronSchedule).To(Equal("1 2 3 * *"))

			Expect(r.Crons.crons.Entries()).To(HaveLen(1))

			err = os.Setenv("TELEMETRY_SCHEDULE", "3 2 1 * *")
			Expect(err).NotTo(HaveOccurred())

			err = r.reconcileScheduledTelemetrySending(ctx, cr)
			Expect(err).NotTo(HaveOccurred())

			job, exists = r.Crons.telemetryJobs.Load(jobName)
			Expect(exists).To(BeTrue())

			telJob, ok = job.(telemetryJob)
			Expect(ok).To(BeTrue())
			Expect(telJob.cronSchedule).To(Equal("3 2 1 * *"))

			Expect(r.Crons.crons.Entries()).To(HaveLen(1))
		})
	})

	Context("When cluster is not ready", func() {
		BeforeEach(func() {
			DeferCleanup(func() {
				err := os.Unsetenv("DISABLE_TELEMETRY")
				Expect(err).NotTo(HaveOccurred())
			})
			err := os.Setenv("DISABLE_TELEMETRY", "false")
			Expect(err).NotTo(HaveOccurred())
		})

		It("should handle cluster non-ready state without error", func() {
			crNamespacedName := types.NamespacedName{Name: crName, Namespace: ns}

			Eventually(func() bool {
				err := k8sClient.Get(ctx, crNamespacedName, cr)
				return err == nil
			}, time.Second*15, time.Millisecond*250).Should(BeTrue())

			cr.Status.State = apiv1.StateInitializing
			Expect(k8sClient.Status().Update(ctx, cr)).Should(Succeed())

			r := reconciler()
			err := r.reconcileScheduledTelemetrySending(ctx, cr)
			Expect(err).NotTo(HaveOccurred())

			jobName := telemetryJobName(cr)
			_, exists := r.Crons.telemetryJobs.Load(jobName)
			Expect(exists).To(BeFalse())

			Expect(r.Crons.crons.Entries()).To(HaveLen(0))
		})
	})
})
