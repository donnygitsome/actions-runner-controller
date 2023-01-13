package actionsgithubcom

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	actionsv1alpha1 "github.com/actions/actions-runner-controller/apis/actions.github.com/v1alpha1"
	"github.com/actions/actions-runner-controller/github/actions/fake"
)

const (
	ephemeralRunnerSetTestTimeout     = time.Second * 5
	ephemeralRunnerSetTestInterval    = time.Millisecond * 250
	ephemeralRunnerSetTestGitHubToken = "gh_token"
)

var _ = Describe("Test EphemeralRunnerSet controller", func() {
	var ctx context.Context
	var cancel context.CancelFunc
	autoScalingNS := new(corev1.Namespace)
	ephemeralRunnerSet := new(actionsv1alpha1.EphemeralRunnerSet)
	configSecret := new(corev1.Secret)

	BeforeEach(func() {
		ctx, cancel = context.WithCancel(context.TODO())
		autoScalingNS = &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: "testns-autoscaling-runnerset" + RandStringRunes(5)},
		}

		err := k8sClient.Create(ctx, autoScalingNS)
		Expect(err).NotTo(HaveOccurred(), "failed to create test namespace for EphemeralRunnerSet")

		configSecret = &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "github-config-secret",
				Namespace: autoScalingNS.Name,
			},
			Data: map[string][]byte{
				"github_token": []byte(ephemeralRunnerSetTestGitHubToken),
			},
		}

		err = k8sClient.Create(ctx, configSecret)
		Expect(err).NotTo(HaveOccurred(), "failed to create config secret")

		mgr, err := ctrl.NewManager(cfg, ctrl.Options{
			Namespace:          autoScalingNS.Name,
			MetricsBindAddress: "0",
		})
		Expect(err).NotTo(HaveOccurred(), "failed to create manager")

		controller := &EphemeralRunnerSetReconciler{
			Client:        mgr.GetClient(),
			Scheme:        mgr.GetScheme(),
			Log:           logf.Log,
			ActionsClient: fake.NewMultiClient(),
		}
		err = controller.SetupWithManager(mgr)
		Expect(err).NotTo(HaveOccurred(), "failed to setup controller")

		ephemeralRunnerSet = &actionsv1alpha1.EphemeralRunnerSet{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-asrs",
				Namespace: autoScalingNS.Name,
			},
			Spec: actionsv1alpha1.EphemeralRunnerSetSpec{
				EphemeralRunnerSpec: actionsv1alpha1.EphemeralRunnerSpec{
					GitHubConfigUrl:    "https://github.com/owner/repo",
					GitHubConfigSecret: configSecret.Name,
					RunnerScaleSetId:   100,
					PodTemplateSpec: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name:  "runner",
									Image: "ghcr.io/actions/runner",
								},
							},
						},
					},
				},
			},
		}

		err = k8sClient.Create(ctx, ephemeralRunnerSet)
		Expect(err).NotTo(HaveOccurred(), "failed to create EphemeralRunnerSet")

		go func() {
			defer GinkgoRecover()

			err := mgr.Start(ctx)
			Expect(err).NotTo(HaveOccurred(), "failed to start manager")
		}()
	})

	AfterEach(func() {
		defer cancel()

		err := k8sClient.Delete(ctx, autoScalingNS)
		Expect(err).NotTo(HaveOccurred(), "failed to delete test namespace for EphemeralRunnerSet")
	})

	Context("When creating a new EphemeralRunnerSet", func() {
		It("It should create/add all required resources for a new EphemeralRunnerSet (finalizer)", func() {
			// Check if finalizer is added
			created := new(actionsv1alpha1.EphemeralRunnerSet)
			Eventually(
				func() (string, error) {
					err := k8sClient.Get(ctx, client.ObjectKey{Name: ephemeralRunnerSet.Name, Namespace: ephemeralRunnerSet.Namespace}, created)
					if err != nil {
						return "", err
					}
					if len(created.Finalizers) == 0 {
						return "", nil
					}
					return created.Finalizers[0], nil
				},
				ephemeralRunnerSetTestTimeout,
				ephemeralRunnerSetTestInterval).Should(BeEquivalentTo(ephemeralRunnerSetFinalizerName), "EphemeralRunnerSet should have a finalizer")

			// Check if the number of ephemeral runners are stay 0
			Consistently(
				func() (int, error) {
					runnerList := new(actionsv1alpha1.EphemeralRunnerList)
					err := k8sClient.List(ctx, runnerList, client.InNamespace(ephemeralRunnerSet.Namespace))
					if err != nil {
						return -1, err
					}

					return len(runnerList.Items), nil
				},
				ephemeralRunnerSetTestTimeout,
				ephemeralRunnerSetTestInterval).Should(BeEquivalentTo(0), "No EphemeralRunner should be created")

			// Check if the status stay 0
			Consistently(
				func() (int, error) {
					runnerSet := new(actionsv1alpha1.EphemeralRunnerSet)
					err := k8sClient.Get(ctx, client.ObjectKey{Name: ephemeralRunnerSet.Name, Namespace: ephemeralRunnerSet.Namespace}, runnerSet)
					if err != nil {
						return -1, err
					}

					return int(runnerSet.Status.CurrentReplicas), nil
				},
				ephemeralRunnerSetTestTimeout,
				ephemeralRunnerSetTestInterval).Should(BeEquivalentTo(0), "EphemeralRunnerSet status should be 0")

			// Scaling up the EphemeralRunnerSet
			updated := created.DeepCopy()
			updated.Spec.Replicas = 5
			err := k8sClient.Update(ctx, updated)
			Expect(err).NotTo(HaveOccurred(), "failed to update EphemeralRunnerSet")

			// Check if the number of ephemeral runners are created
			Eventually(
				func() (int, error) {
					runnerList := new(actionsv1alpha1.EphemeralRunnerList)
					err := k8sClient.List(ctx, runnerList, client.InNamespace(ephemeralRunnerSet.Namespace))
					if err != nil {
						return -1, err
					}

					return len(runnerList.Items), nil
				},
				ephemeralRunnerSetTestTimeout,
				ephemeralRunnerSetTestInterval).Should(BeEquivalentTo(5), "5 EphemeralRunner should be created")

			// Check if the status is updated
			Eventually(
				func() (int, error) {
					runnerSet := new(actionsv1alpha1.EphemeralRunnerSet)
					err := k8sClient.Get(ctx, client.ObjectKey{Name: ephemeralRunnerSet.Name, Namespace: ephemeralRunnerSet.Namespace}, runnerSet)
					if err != nil {
						return -1, err
					}

					return int(runnerSet.Status.CurrentReplicas), nil
				},
				ephemeralRunnerSetTestTimeout,
				ephemeralRunnerSetTestInterval).Should(BeEquivalentTo(5), "EphemeralRunnerSet status should be 5")
		})
	})

	Context("When deleting a new EphemeralRunnerSet", func() {
		It("It should cleanup all resources for a deleting EphemeralRunnerSet before removing it", func() {
			created := new(actionsv1alpha1.EphemeralRunnerSet)
			err := k8sClient.Get(ctx, client.ObjectKey{Name: ephemeralRunnerSet.Name, Namespace: ephemeralRunnerSet.Namespace}, created)
			Expect(err).NotTo(HaveOccurred(), "failed to get EphemeralRunnerSet")

			// Scale up the EphemeralRunnerSet
			updated := created.DeepCopy()
			updated.Spec.Replicas = 5
			err = k8sClient.Update(ctx, updated)
			Expect(err).NotTo(HaveOccurred(), "failed to update EphemeralRunnerSet")

			// Wait for the EphemeralRunnerSet to be scaled up
			Eventually(
				func() (int, error) {
					runnerList := new(actionsv1alpha1.EphemeralRunnerList)
					err := k8sClient.List(ctx, runnerList, client.InNamespace(ephemeralRunnerSet.Namespace))
					if err != nil {
						return -1, err
					}

					return len(runnerList.Items), nil
				},
				ephemeralRunnerSetTestTimeout,
				ephemeralRunnerSetTestInterval).Should(BeEquivalentTo(5), "5 EphemeralRunner should be created")

			// Delete the EphemeralRunnerSet
			err = k8sClient.Delete(ctx, created)
			Expect(err).NotTo(HaveOccurred(), "failed to delete EphemeralRunnerSet")

			// Check if all ephemeral runners are deleted
			Eventually(
				func() (int, error) {
					runnerList := new(actionsv1alpha1.EphemeralRunnerList)
					err := k8sClient.List(ctx, runnerList, client.InNamespace(ephemeralRunnerSet.Namespace))
					if err != nil {
						return -1, err
					}

					return len(runnerList.Items), nil
				},
				ephemeralRunnerSetTestTimeout,
				ephemeralRunnerSetTestInterval).Should(BeEquivalentTo(0), "All EphemeralRunner should be deleted")

			// Check if the EphemeralRunnerSet is deleted
			Eventually(
				func() error {
					deleted := new(actionsv1alpha1.EphemeralRunnerSet)
					err = k8sClient.Get(ctx, client.ObjectKey{Name: ephemeralRunnerSet.Name, Namespace: ephemeralRunnerSet.Namespace}, deleted)
					if err != nil {
						if kerrors.IsNotFound(err) {
							return nil
						}

						return err
					}

					return fmt.Errorf("EphemeralRunnerSet is not deleted")
				},
				ephemeralRunnerSetTestTimeout,
				ephemeralRunnerSetTestInterval).Should(Succeed(), "EphemeralRunnerSet should be deleted")
		})
	})

	Context("When a new EphemeralRunnerSet scale up and down", func() {
		It("It should delete finished EphemeralRunner and create new EphemeralRunner", func() {
			created := new(actionsv1alpha1.EphemeralRunnerSet)
			err := k8sClient.Get(ctx, client.ObjectKey{Name: ephemeralRunnerSet.Name, Namespace: ephemeralRunnerSet.Namespace}, created)
			Expect(err).NotTo(HaveOccurred(), "failed to get EphemeralRunnerSet")

			// Scale up the EphemeralRunnerSet
			updated := created.DeepCopy()
			updated.Spec.Replicas = 5
			err = k8sClient.Update(ctx, updated)
			Expect(err).NotTo(HaveOccurred(), "failed to update EphemeralRunnerSet")

			// Wait for the EphemeralRunnerSet to be scaled up
			runnerList := new(actionsv1alpha1.EphemeralRunnerList)
			Eventually(
				func() (int, error) {
					err := k8sClient.List(ctx, runnerList, client.InNamespace(ephemeralRunnerSet.Namespace))
					if err != nil {
						return -1, err
					}

					return len(runnerList.Items), nil
				},
				ephemeralRunnerSetTestTimeout,
				ephemeralRunnerSetTestInterval).Should(BeEquivalentTo(5), "5 EphemeralRunner should be created")

			// Set status to simulate a configured EphemeralRunner
			for i, runner := range runnerList.Items {
				updatedRunner := runner.DeepCopy()
				updatedRunner.Status.Phase = corev1.PodRunning
				updatedRunner.Status.RunnerId = i + 100
				err = k8sClient.Status().Patch(ctx, updatedRunner, client.MergeFrom(&runner))
				Expect(err).NotTo(HaveOccurred(), "failed to update EphemeralRunner")
			}

			// Mark one of the EphemeralRunner as finished
			finishedRunner := runnerList.Items[4].DeepCopy()
			finishedRunner.Status.Phase = corev1.PodSucceeded
			err = k8sClient.Status().Patch(ctx, finishedRunner, client.MergeFrom(&runnerList.Items[4]))
			Expect(err).NotTo(HaveOccurred(), "failed to update EphemeralRunner")

			// Wait for the finished EphemeralRunner to be deleted
			Eventually(
				func() error {
					runnerList := new(actionsv1alpha1.EphemeralRunnerList)
					err := k8sClient.List(ctx, runnerList, client.InNamespace(ephemeralRunnerSet.Namespace))
					if err != nil {
						return err
					}

					for _, runner := range runnerList.Items {
						if runner.Name == finishedRunner.Name {
							return fmt.Errorf("EphemeralRunner is not deleted")
						}
					}

					return nil
				},
				ephemeralRunnerSetTestTimeout,
				ephemeralRunnerSetTestInterval).Should(Succeed(), "Finished EphemeralRunner should be deleted")

			// We should still have the EphemeralRunnerSet scale up
			runnerList = new(actionsv1alpha1.EphemeralRunnerList)
			Eventually(
				func() (int, error) {
					err := k8sClient.List(ctx, runnerList, client.InNamespace(ephemeralRunnerSet.Namespace))
					if err != nil {
						return -1, err
					}

					return len(runnerList.Items), nil
				},
				ephemeralRunnerSetTestTimeout,
				ephemeralRunnerSetTestInterval).Should(BeEquivalentTo(5), "5 EphemeralRunner should be created")

			// Set status to simulate a configured EphemeralRunner
			for i, runner := range runnerList.Items {
				updatedRunner := runner.DeepCopy()
				updatedRunner.Status.Phase = corev1.PodRunning
				updatedRunner.Status.RunnerId = i + 100
				err = k8sClient.Status().Patch(ctx, updatedRunner, client.MergeFrom(&runner))
				Expect(err).NotTo(HaveOccurred(), "failed to update EphemeralRunner")
			}

			// Scale down the EphemeralRunnerSet
			updated = created.DeepCopy()
			updated.Spec.Replicas = 3
			err = k8sClient.Patch(ctx, updated, client.MergeFrom(created))
			Expect(err).NotTo(HaveOccurred(), "failed to update EphemeralRunnerSet")

			// Wait for the EphemeralRunnerSet to be scaled down
			runnerList = new(actionsv1alpha1.EphemeralRunnerList)
			Eventually(
				func() (int, error) {
					err := k8sClient.List(ctx, runnerList, client.InNamespace(ephemeralRunnerSet.Namespace))
					if err != nil {
						return -1, err
					}

					return len(runnerList.Items), nil
				},
				ephemeralRunnerSetTestTimeout,
				ephemeralRunnerSetTestInterval).Should(BeEquivalentTo(3), "3 EphemeralRunner should be created")

			// We will not scale down runner that is running jobs
			runningRunner := runnerList.Items[0].DeepCopy()
			runningRunner.Status.JobRequestId = 1000
			err = k8sClient.Status().Patch(ctx, runningRunner, client.MergeFrom(&runnerList.Items[0]))
			Expect(err).NotTo(HaveOccurred(), "failed to update EphemeralRunner")

			runningRunner = runnerList.Items[1].DeepCopy()
			runningRunner.Status.JobRequestId = 1001
			err = k8sClient.Status().Patch(ctx, runningRunner, client.MergeFrom(&runnerList.Items[0]))
			Expect(err).NotTo(HaveOccurred(), "failed to update EphemeralRunner")

			// Scale down to 1
			updated = created.DeepCopy()
			updated.Spec.Replicas = 1
			err = k8sClient.Patch(ctx, updated, client.MergeFrom(created))
			Expect(err).NotTo(HaveOccurred(), "failed to update EphemeralRunnerSet")

			// Wait for the EphemeralRunnerSet to be scaled down to 2 since we still have 2 runner running jobs
			runnerList = new(actionsv1alpha1.EphemeralRunnerList)
			Eventually(
				func() (int, error) {
					err := k8sClient.List(ctx, runnerList, client.InNamespace(ephemeralRunnerSet.Namespace))
					if err != nil {
						return -1, err
					}

					return len(runnerList.Items), nil
				},
				ephemeralRunnerSetTestTimeout,
				ephemeralRunnerSetTestInterval).Should(BeEquivalentTo(2), "2 EphemeralRunner should be created")

			// We will not scale down failed runner
			failedRunner := runnerList.Items[0].DeepCopy()
			failedRunner.Status.Phase = corev1.PodFailed
			err = k8sClient.Status().Patch(ctx, failedRunner, client.MergeFrom(&runnerList.Items[0]))
			Expect(err).NotTo(HaveOccurred(), "failed to update EphemeralRunner")

			// Scale down to 0
			updated = created.DeepCopy()
			updated.Spec.Replicas = 0
			err = k8sClient.Patch(ctx, updated, client.MergeFrom(created))
			Expect(err).NotTo(HaveOccurred(), "failed to update EphemeralRunnerSet")

			// We should not scale down the EphemeralRunnerSet since we still have 1 runner running job and 1 failed runner
			runnerList = new(actionsv1alpha1.EphemeralRunnerList)
			Consistently(
				func() (int, error) {
					err := k8sClient.List(ctx, runnerList, client.InNamespace(ephemeralRunnerSet.Namespace))
					if err != nil {
						return -1, err
					}

					return len(runnerList.Items), nil
				},
				ephemeralRunnerSetTestTimeout,
				ephemeralRunnerSetTestInterval).Should(BeEquivalentTo(2), "2 EphemeralRunner should be created")

			// We will scale down to 0 when the running job is completed and the failed runner is deleted
			runningRunner = runnerList.Items[1].DeepCopy()
			runningRunner.Status.Phase = corev1.PodSucceeded
			err = k8sClient.Status().Patch(ctx, runningRunner, client.MergeFrom(&runnerList.Items[1]))
			Expect(err).NotTo(HaveOccurred(), "failed to update EphemeralRunner")

			err = k8sClient.Delete(ctx, &runnerList.Items[0])
			Expect(err).NotTo(HaveOccurred(), "failed to delete EphemeralRunner")

			// Wait for the EphemeralRunnerSet to be scaled down to 0
			runnerList = new(actionsv1alpha1.EphemeralRunnerList)
			Eventually(
				func() (int, error) {
					err := k8sClient.List(ctx, runnerList, client.InNamespace(ephemeralRunnerSet.Namespace))
					if err != nil {
						return -1, err
					}

					return len(runnerList.Items), nil
				},
				ephemeralRunnerSetTestTimeout,
				ephemeralRunnerSetTestInterval).Should(BeEquivalentTo(0), "0 EphemeralRunner should be created")
		})
	})
})
