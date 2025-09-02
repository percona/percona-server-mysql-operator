package ps

import (
	"context"
	"os"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/robfig/cron/v3"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	apiv1alpha1 "github.com/percona/percona-server-mysql-operator/api/v1alpha1"
)

var _ = Describe("Reconcile telemetry sending", Ordered, func() {
	ctx := context.Background()

	const crName = "telemetry-test"
	const ns = crName
	crNamespacedName := types.NamespacedName{Name: crName, Namespace: ns}

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
		// configuring a dummy url so that ginkgo test do not make an actual API call.
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
	})

	Context("When telemetry is disabled", func() {
		BeforeEach(func() {
			DeferCleanup(func() {
				err := os.Unsetenv("DISABLE_TELEMETRY")
				Expect(err).NotTo(HaveOccurred())
			})
			err := os.Setenv("DISABLE_TELEMETRY", "true")
			Expect(err).NotTo(HaveOccurred())
		})

		It("should not create telemetry job when telemetry is disabled", func() {
			r := reconciler()

			err := r.reconcileScheduledTelemetrySending(ctx, cr)
			Expect(err).NotTo(HaveOccurred())

			jobName := telemetryJobName(cr)
			_, exists := r.Crons.telemetryJobs.Load(jobName)
			Expect(exists).To(BeFalse())
		})

		It("should remove existing telemetry job when telemetry is disabled", func() {
			r := reconciler()
			jobName := telemetryJobName(cr)

			r.Crons.telemetryJobs.Store(jobName, telemetryJob{
				scheduleJob:  scheduleJob{jobID: cron.EntryID(1)},
				cronSchedule: "0 0 * * *",
			})

			err := r.reconcileScheduledTelemetrySending(ctx, cr)
			Expect(err).NotTo(HaveOccurred())

			_, exists := r.Crons.telemetryJobs.Load(jobName)
			Expect(exists).To(BeFalse())
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

			originalJobID := cron.EntryID(1)
			r.Crons.telemetryJobs.Store(jobName, telemetryJob{
				scheduleJob:  scheduleJob{jobID: originalJobID},
				cronSchedule: "0 0 * * *",
			})

			err = r.reconcileScheduledTelemetrySending(ctx, cr)
			Expect(err).NotTo(HaveOccurred())

			job, exists := r.Crons.telemetryJobs.Load(jobName)
			Expect(exists).To(BeTrue())

			telemetryJobVal, ok := job.(telemetryJob)
			Expect(ok).To(BeTrue())
			Expect(telemetryJobVal.jobID).To(Equal(originalJobID))
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

			originalJobID := cron.EntryID(1)
			r.Crons.telemetryJobs.Store(jobName, telemetryJob{
				scheduleJob:  scheduleJob{jobID: originalJobID},
				cronSchedule: "0 1 * * *",
			})

			err = r.reconcileScheduledTelemetrySending(ctx, cr)
			Expect(err).NotTo(HaveOccurred())

			job, exists := r.Crons.telemetryJobs.Load(jobName)
			Expect(exists).To(BeTrue())

			telemetryJob, ok := job.(telemetryJob)
			Expect(ok).To(BeTrue())
			Expect(telemetryJob.cronSchedule).To(Equal("1 2 3 * *"))
		})
	})

	Context("When cluster is ready", func() {
		BeforeEach(func() {
			DeferCleanup(func() {
				err := os.Unsetenv("DISABLE_TELEMETRY")
				Expect(err).NotTo(HaveOccurred())
			})
			err := os.Setenv("DISABLE_TELEMETRY", "false")
			Expect(err).NotTo(HaveOccurred())
		})

		It("should handle cluster ready state without error", func() {
			Eventually(func() bool {
				err := k8sClient.Get(ctx, crNamespacedName, cr)
				return err == nil
			}, time.Second*15, time.Millisecond*250).Should(BeTrue())

			cr.Status.State = apiv1alpha1.StateReady
			Expect(k8sClient.Status().Update(ctx, cr)).Should(Succeed())

			r := reconciler()
			err := r.reconcileScheduledTelemetrySending(ctx, cr)
			Expect(err).NotTo(HaveOccurred())

			jobName := telemetryJobName(cr)
			_, exists := r.Crons.telemetryJobs.Load(jobName)
			Expect(exists).To(BeTrue())
		})
	})
})
