package controller

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	rgv1 "github.com/szuecs/routegroup-client/apis/zalando.org/v1"
	zv1 "github.com/zalando-incubator/stackset-controller/pkg/apis/zalando.org/v1"
	"github.com/zalando-incubator/stackset-controller/pkg/core"
	apps "k8s.io/api/apps/v1"
	autoscaling "k8s.io/api/autoscaling/v2beta2"
	v1 "k8s.io/api/core/v1"
	networking "k8s.io/api/networking/v1beta1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
)

func TestGetOwnerUID(t *testing.T) {
	objectMeta := metav1.ObjectMeta{
		OwnerReferences: []metav1.OwnerReference{
			{
				UID: types.UID("x"),
			},
		},
	}

	uid, ok := getOwnerUID(objectMeta)
	assert.Equal(t, types.UID("x"), uid)
	assert.True(t, ok)

	uid, ok = getOwnerUID(metav1.ObjectMeta{})
	assert.Equal(t, types.UID(""), uid)
	assert.False(t, ok)
}

func TestCollectResources(t *testing.T) {
	testStacksetA := testStackset("foo", "default", "123")
	testStacksetB := testStackset("bar", "namespace", "999")

	testStackA1 := testStack("foo-v1", testStacksetA.Namespace, "abc1", testStacksetA)
	testStackA2 := testStack("foo-v2", testStacksetA.Namespace, "abc2", testStacksetA)
	testDeploymentA2 := apps.Deployment{ObjectMeta: stackOwned(testStackA2)}
	testStackB1 := testStack("bar-v1", testStacksetB.Namespace, "def3", testStacksetB)

	testPrescalingStackset := testStackset("baz", "namespace", "456")
	testPrescalingStackset.Annotations = map[string]string{PrescaleStacksAnnotationKey: ""}

	testOrphanMeta := stackOwned(testStack("nonexistent", "default", "xxx", zv1.StackSet{}))
	testUnownedA1Meta := metav1.ObjectMeta{Name: testStackA1.Name, Namespace: testStackA1.Namespace}
	testUnownedBMeta := metav1.ObjectMeta{Name: testStacksetB.Name, Namespace: testStacksetB.Namespace}

	testPrescalingCustomStackset := testStackset("foobaz", "namespace", "789")
	testPrescalingCustomStackset.Annotations = map[string]string{PrescaleStacksAnnotationKey: "", ResetHPAMinReplicasDelayAnnotationKey: "30s"}

	for _, tc := range []struct {
		name        string
		stacksets   []zv1.StackSet
		stacks      []zv1.Stack
		deployments []apps.Deployment
		ingresses   []networking.Ingress
		routegroups []rgv1.RouteGroup
		services    []v1.Service
		hpas        []autoscaling.HorizontalPodAutoscaler
		expected    map[types.UID]*core.StackSetContainer
	}{
		{
			name: "works correctly without any resources",
			stacksets: []zv1.StackSet{
				testStacksetA,
				testPrescalingStackset,
				testPrescalingCustomStackset,
			},
			expected: map[types.UID]*core.StackSetContainer{
				testStacksetA.UID: {
					StackSet:          &testStacksetA,
					StackContainers:   map[types.UID]*core.StackContainer{},
					TrafficReconciler: &core.SimpleTrafficReconciler{},
				},
				testPrescalingStackset.UID: {
					StackSet:        &testPrescalingStackset,
					StackContainers: map[types.UID]*core.StackContainer{},
					TrafficReconciler: &core.PrescalingTrafficReconciler{
						ResetHPAMinReplicasTimeout: defaultResetMinReplicasDelay,
					},
				},
				testPrescalingCustomStackset.UID: {
					StackSet:        &testPrescalingCustomStackset,
					StackContainers: map[types.UID]*core.StackContainer{},
					TrafficReconciler: &core.PrescalingTrafficReconciler{
						ResetHPAMinReplicasTimeout: 30 * time.Second,
					},
				},
			},
		},
		{
			name:      "stacks are collected even without resources",
			stacksets: []zv1.StackSet{testStacksetA, testStacksetB},
			stacks:    []zv1.Stack{testStackA1, testStackA2, testStackB1},
			expected: map[types.UID]*core.StackSetContainer{
				testStacksetA.UID: {
					StackSet: &testStacksetA,
					StackContainers: map[types.UID]*core.StackContainer{
						testStackA1.UID: {
							Stack: &testStackA1,
						},
						testStackA2.UID: {
							Stack: &testStackA2,
						},
					},
					TrafficReconciler: &core.SimpleTrafficReconciler{},
				},
				testStacksetB.UID: {
					StackSet: &testStacksetB,
					StackContainers: map[types.UID]*core.StackContainer{
						testStackB1.UID: {
							Stack: &testStackB1,
						},
					},
					TrafficReconciler: &core.SimpleTrafficReconciler{},
				},
			},
		},
		{
			name:      "all resources are collected",
			stacksets: []zv1.StackSet{testStacksetA, testStacksetB},
			stacks:    []zv1.Stack{testStackA1, testStackA2, testStackB1},
			deployments: []apps.Deployment{
				testDeploymentA2,                // stack owned
				{ObjectMeta: testOrphanMeta},    // owned by unknown stack
				{ObjectMeta: testUnownedA1Meta}, // same name, but not owned by a stack
			},
			ingresses: []networking.Ingress{
				{ObjectMeta: stackOwned(testStackA2)},      // stack owned
				{ObjectMeta: testOrphanMeta},               // owned by unknown stack
				{ObjectMeta: testUnownedA1Meta},            // same name, but not owned by a stack
				{ObjectMeta: stacksetOwned(testStacksetA)}, // owned by stackset
				{ObjectMeta: testUnownedBMeta},             // same name, but not owned by a stackset
			},
			routegroups: []rgv1.RouteGroup{
				{ObjectMeta: stackOwned(testStackA2)},      // stack owned
				{ObjectMeta: testOrphanMeta},               // owned by unknown stack
				{ObjectMeta: testUnownedA1Meta},            // same name, but not owned by a stack
				{ObjectMeta: stacksetOwned(testStacksetA)}, // owned by stackset
				{ObjectMeta: testUnownedBMeta},             // same name, but not owned by a stackset
			},
			services: []v1.Service{
				{ObjectMeta: stackOwned(testStackA2)}, // stack owned
				{ObjectMeta: testOrphanMeta},          // owned by unknown stack
				{ObjectMeta: testUnownedA1Meta},       // same name, but not owned by a stack
			},
			hpas: []autoscaling.HorizontalPodAutoscaler{
				{ObjectMeta: stackOwned(testStackA2)}, // stack owned
				{ObjectMeta: testOrphanMeta},          // owned by unknown stack
				{ObjectMeta: testUnownedA1Meta},       // same name, but not owned by a stack
			},
			expected: map[types.UID]*core.StackSetContainer{
				testStacksetA.UID: {
					StackSet: &testStacksetA,
					StackContainers: map[types.UID]*core.StackContainer{
						testStackA1.UID: {
							Stack: &testStackA1,
						},
						testStackA2.UID: {
							Stack: &testStackA2,
							Resources: core.StackResources{
								Deployment: &apps.Deployment{ObjectMeta: stackOwned(testStackA2)},
								HPA:        &autoscaling.HorizontalPodAutoscaler{ObjectMeta: stackOwned(testStackA2)},
								Service:    &v1.Service{ObjectMeta: stackOwned(testStackA2)},
								Ingress:    &networking.Ingress{ObjectMeta: stackOwned(testStackA2)},
								RouteGroup: &rgv1.RouteGroup{ObjectMeta: stackOwned(testStackA2)},
							},
						},
					},
					Ingress:           &networking.Ingress{ObjectMeta: stacksetOwned(testStacksetA)},
					RouteGroup:        &rgv1.RouteGroup{ObjectMeta: stacksetOwned(testStacksetA)},
					TrafficReconciler: &core.SimpleTrafficReconciler{},
				},
				testStacksetB.UID: {
					StackSet: &testStacksetB,
					StackContainers: map[types.UID]*core.StackContainer{
						testStackB1.UID: {
							Stack: &testStackB1,
						},
					},
					TrafficReconciler: &core.SimpleTrafficReconciler{},
				},
			},
		},
		{
			name:      "service and HPA owned by the deployment are supported as well",
			stacksets: []zv1.StackSet{testStacksetA},
			stacks:    []zv1.Stack{testStackA2},
			deployments: []apps.Deployment{
				testDeploymentA2, // stack owned
			},
			ingresses: []networking.Ingress{
				{ObjectMeta: deploymentOwned(testDeploymentA2)}, // deployment owned, not supported
			},
			routegroups: []rgv1.RouteGroup{
				{ObjectMeta: deploymentOwned(testDeploymentA2)}, // deployment owned, not supported
			},
			services: []v1.Service{
				{ObjectMeta: deploymentOwned(testDeploymentA2)}, // deployment owned
			},
			hpas: []autoscaling.HorizontalPodAutoscaler{
				{ObjectMeta: deploymentOwned(testDeploymentA2)}, // deployment owned, not supported
			},
			expected: map[types.UID]*core.StackSetContainer{
				testStacksetA.UID: {
					StackSet: &testStacksetA,
					StackContainers: map[types.UID]*core.StackContainer{
						testStackA2.UID: {
							Stack: &testStackA2,
							Resources: core.StackResources{
								Deployment: &apps.Deployment{ObjectMeta: stackOwned(testStackA2)},
								HPA:        &autoscaling.HorizontalPodAutoscaler{ObjectMeta: deploymentOwned(testDeploymentA2)},
								Service:    &v1.Service{ObjectMeta: deploymentOwned(testDeploymentA2)},
							},
						},
					},
					TrafficReconciler: &core.SimpleTrafficReconciler{},
				},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := NewTestEnvironment()

			err := env.CreateStacksets(context.Background(), tc.stacksets)
			require.NoError(t, err)

			err = env.CreateStacks(context.Background(), tc.stacks)
			require.NoError(t, err)

			err = env.CreateDeployments(context.Background(), tc.deployments)
			require.NoError(t, err)

			err = env.CreateIngresses(context.Background(), tc.ingresses)
			require.NoError(t, err)

			err = env.CreateRouteGroups(context.Background(), tc.routegroups)
			require.NoError(t, err)

			err = env.CreateServices(context.Background(), tc.services)
			require.NoError(t, err)

			err = env.CreateHPAs(context.Background(), tc.hpas)
			require.NoError(t, err)

			resources, err := env.controller.collectResources(context.Background())
			require.NoError(t, err)
			require.Equal(t, tc.expected, resources)
		})
	}
}

func TestCreateCurrentStack(t *testing.T) {
	env := NewTestEnvironment()

	replicas := int32(1)

	stackset := testStackset("foo", "default", "123")
	stackset.Spec.StackTemplate.Spec = zv1.StackSpecTemplate{
		Version: "v1",
		StackSpec: zv1.StackSpec{
			Replicas: &replicas,
			PodTemplate: zv1.PodTemplateSpec{
				Spec: v1.PodSpec{
					Containers: []v1.Container{
						{
							Name:  "foo",
							Image: "nginx",
						},
					},
				},
			},
		},
	}

	err := env.CreateStacksets(context.Background(), []zv1.StackSet{stackset})
	require.NoError(t, err)

	_, err = env.client.ZalandoV1().Stacks(stackset.Namespace).Get(context.Background(), "foo-v1", metav1.GetOptions{})
	require.True(t, errors.IsNotFound(err))

	container := &core.StackSetContainer{
		StackSet:          &stackset,
		StackContainers:   map[types.UID]*core.StackContainer{},
		TrafficReconciler: &core.SimpleTrafficReconciler{},
	}

	// Check that the stack is created and the container is updated afterwards
	err = env.controller.CreateCurrentStack(context.Background(), container)
	require.NoError(t, err)

	stack, err := env.client.ZalandoV1().Stacks(stackset.Namespace).Get(context.Background(), "foo-v1", metav1.GetOptions{})
	require.NoError(t, err)

	stack.APIVersion = "zalando.org/v1"
	stack.Kind = "Stack"

	require.Equal(t, stackset.Spec.StackTemplate.Spec.StackSpec, stack.Spec)
	require.Equal(t, map[types.UID]*core.StackContainer{
		stack.UID: {
			Stack: stack,
		},
	}, container.StackContainers)
	require.Equal(t, "v1", container.StackSet.Status.ObservedStackVersion)

	// Check that we don't create the stack if not needed
	stackset.Status.ObservedStackVersion = "v2"
	stackset.Spec.StackTemplate.Spec.Version = "v2"

	err = env.controller.CreateCurrentStack(context.Background(), container)
	require.NoError(t, err)

	_, err = env.client.ZalandoV1().Stacks(stackset.Namespace).Get(context.Background(), "foo-v2", metav1.GetOptions{})
	require.True(t, errors.IsNotFound(err))
}

func TestCleanupOldStacks(t *testing.T) {
	env := NewTestEnvironment()

	stackset := testStackset("foo", "default", "123")
	testStack1 := testStack("foo-v1", stackset.Namespace, "abc1", stackset)
	testStack2 := testStack("foo-v2", stackset.Namespace, "abc2", stackset)
	testStack3 := testStack("foo-v3", stackset.Namespace, "abc3", stackset)
	testStack4 := testStack("foo-v4", stackset.Namespace, "abc4", stackset)

	err := env.CreateStacksets(context.Background(), []zv1.StackSet{stackset})
	require.NoError(t, err)

	err = env.CreateStacks(context.Background(), []zv1.Stack{testStack1, testStack2, testStack3, testStack4})
	require.NoError(t, err)

	container := &core.StackSetContainer{
		StackSet: &stackset,
		StackContainers: map[types.UID]*core.StackContainer{
			testStack1.UID: {
				Stack:          &testStack1,
				PendingRemoval: true,
			},
			testStack2.UID: {
				Stack:          &testStack2,
				PendingRemoval: true,
			},
			testStack3.UID: {
				Stack:          &testStack3,
				PendingRemoval: false,
			},
			testStack4.UID: {
				Stack:          &testStack4,
				PendingRemoval: false,
			},
		},
		TrafficReconciler: &core.SimpleTrafficReconciler{},
	}

	err = env.controller.CleanupOldStacks(context.Background(), container)
	require.NoError(t, err)

	result, err := env.client.ZalandoV1().Stacks(stackset.Namespace).List(context.Background(), metav1.ListOptions{})
	require.NoError(t, err)
	require.Equal(t, []zv1.Stack{testStack3, testStack4}, result.Items)
}

func TestReconcileStackSetDesiredTraffic(t *testing.T) {
	stackMeta := metav1.ObjectMeta{
		Name: "foo",
		UID:  "abc1234",
	}

	sampleTraffic := []*zv1.DesiredTraffic{
		{StackName: "foo-1", Weight: 50},
	}
	updatedTraffic := []*zv1.DesiredTraffic{
		{StackName: "foo-1", Weight: 30},
		{StackName: "foo-2", Weight: 70},
	}

	for _, tc := range []struct {
		name     string
		existing zv1.StackSet
		updated  []*zv1.DesiredTraffic
		expected zv1.StackSet
	}{
		{
			name: "stack is populated with traffic weights",
			existing: zv1.StackSet{
				ObjectMeta: stackMeta,
			},
			updated: updatedTraffic,
			expected: zv1.StackSet{
				ObjectMeta: stackMeta,
				Spec: zv1.StackSetSpec{
					Traffic: updatedTraffic,
				},
			},
		},
		{
			name: "stack is populated with new traffic weights",
			existing: zv1.StackSet{
				ObjectMeta: stackMeta,
				Spec: zv1.StackSetSpec{
					Traffic: sampleTraffic,
				},
			},
			updated: updatedTraffic,
			expected: zv1.StackSet{
				ObjectMeta: stackMeta,
				Spec: zv1.StackSetSpec{
					Traffic: updatedTraffic,
				},
			},
		},
		{
			name: "traffic weights are removed",
			existing: zv1.StackSet{
				ObjectMeta: stackMeta,
				Spec: zv1.StackSetSpec{
					Traffic: sampleTraffic,
				},
			},
			updated: nil,
			expected: zv1.StackSet{
				ObjectMeta: stackMeta,
				Spec:       zv1.StackSetSpec{},
			},
		},
	} {
		env := NewTestEnvironment()

		err := env.CreateStacksets(context.Background(), []zv1.StackSet{tc.existing})
		require.NoError(t, err)

		err = env.controller.ReconcileStackSetDesiredTraffic(context.Background(), &tc.existing, func() []*zv1.DesiredTraffic {
			return tc.updated
		})
		require.NoError(t, err)

		result, err := env.client.ZalandoV1().StackSets(tc.expected.Namespace).Get(context.Background(), tc.expected.Name, metav1.GetOptions{})
		require.NoError(t, err)
		require.EqualValues(t, tc.expected, *result)
	}
}

func TestReconcileStackSetIngress(t *testing.T) {
	exampleRules := []networking.IngressRule{
		{
			Host: "example.org",
			IngressRuleValue: networking.IngressRuleValue{
				HTTP: &networking.HTTPIngressRuleValue{
					Paths: []networking.HTTPIngressPath{
						{
							Path: "/",
							Backend: networking.IngressBackend{
								ServiceName: "foo",
								ServicePort: intstr.FromInt(80),
							},
						},
					},
				},
			},
		},
	}
	exampleUpdatedRules := []networking.IngressRule{
		{
			Host: "example.com",
			IngressRuleValue: networking.IngressRuleValue{
				HTTP: &networking.HTTPIngressRuleValue{
					Paths: []networking.HTTPIngressPath{
						{
							Path: "/",
							Backend: networking.IngressBackend{
								ServiceName: "bar",
								ServicePort: intstr.FromInt(8181),
							},
						},
					},
				},
			},
		},
	}

	withAnnotations := func(meta metav1.ObjectMeta, annotations map[string]string) metav1.ObjectMeta {
		updated := meta.DeepCopy()
		if updated.Annotations == nil {
			updated.Annotations = map[string]string{}
		}
		for k, v := range annotations {
			updated.Annotations[k] = v
		}
		return *updated
	}

	for _, tc := range []struct {
		name           string
		existing       *networking.Ingress
		routegroupSpec *zv1.RouteGroupSpec
		routegroup     *rgv1.RouteGroup
		updated        *networking.Ingress
		expected       *networking.Ingress
	}{
		{
			name: "ingress is created if it doesn't exist",
			updated: &networking.Ingress{
				ObjectMeta: stacksetOwned(testStackSet),
				Spec: networking.IngressSpec{
					Rules: exampleRules,
				},
			},
			expected: &networking.Ingress{
				ObjectMeta: stacksetOwned(testStackSet),
				Spec: networking.IngressSpec{
					Rules: exampleRules,
				},
			},
		},
		{
			name: "ingress is removed if it is no longer needed",
			existing: &networking.Ingress{
				ObjectMeta: stacksetOwned(testStackSet),
				Spec: networking.IngressSpec{
					Rules: exampleRules,
				},
			},
			updated:  nil,
			expected: nil,
		},
		{
			name: "ingress is updated if the spec is changed",
			existing: &networking.Ingress{
				ObjectMeta: stacksetOwned(testStackSet),
				Spec: networking.IngressSpec{
					Rules: exampleRules,
				},
			},
			updated: &networking.Ingress{
				ObjectMeta: stacksetOwned(testStackSet),
				Spec: networking.IngressSpec{
					Rules: exampleUpdatedRules,
				},
			},
			expected: &networking.Ingress{
				ObjectMeta: stacksetOwned(testStackSet),
				Spec: networking.IngressSpec{
					Rules: exampleUpdatedRules,
				},
			},
		},
		{
			name: "ingress is updated if the annotations change",
			existing: &networking.Ingress{
				ObjectMeta: stacksetOwned(testStackSet),
				Spec: networking.IngressSpec{
					Rules: exampleRules,
				},
			},
			updated: &networking.Ingress{
				ObjectMeta: withAnnotations(stacksetOwned(testStackSet), map[string]string{"foo": "bar"}),
				Spec: networking.IngressSpec{
					Rules: exampleRules,
				},
			},
			expected: &networking.Ingress{
				ObjectMeta: withAnnotations(stacksetOwned(testStackSet), map[string]string{"foo": "bar"}),
				Spec: networking.IngressSpec{
					Rules: exampleRules,
				},
			},
		},
		{
			name: "ingress is not rolled back if the server injects some defaults",
			existing: &networking.Ingress{
				ObjectMeta: stacksetOwned(testStackSet),
				Spec: networking.IngressSpec{
					Rules: exampleRules,
				},
			},
			updated: &networking.Ingress{
				ObjectMeta: stacksetOwned(testStackSet),
				Spec: networking.IngressSpec{
					Backend: &networking.IngressBackend{
						ServiceName: "test",
					},
					Rules: exampleRules,
				},
			},
			expected: &networking.Ingress{
				ObjectMeta: stacksetOwned(testStackSet),
				Spec: networking.IngressSpec{
					Backend: &networking.IngressBackend{
						ServiceName: "test",
					},
					Rules: exampleRules,
				},
			},
		},
		{
			// TODO: take into account RouteGroup UpdatedTimestamp
			name: "ingress is not removed if RouteGroup is too young",
			existing: &networking.Ingress{
				ObjectMeta: stacksetOwned(testStackSet),
				Spec: networking.IngressSpec{
					Rules: exampleRules,
				},
			},
			routegroupSpec: &zv1.RouteGroupSpec{
				Hosts: []string{"example.org"},
			},
			routegroup: &rgv1.RouteGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name: testStackSet.Name,
					Annotations: map[string]string{
						core.StacksetControllerUpdateTimestampAnnotationkey: time.Now().Format(time.RFC3339),
					},
				},
			},
			updated: nil,
			expected: &networking.Ingress{
				ObjectMeta: stacksetOwned(testStackSet),
				Spec: networking.IngressSpec{
					Rules: exampleRules,
				},
			},
		},
		{
			name: "ingress is not removed if RouteGroup does not have the updatedTimestamp",
			existing: &networking.Ingress{
				ObjectMeta: stacksetOwned(testStackSet),
				Spec: networking.IngressSpec{
					Rules: exampleRules,
				},
			},
			routegroupSpec: &zv1.RouteGroupSpec{
				Hosts: []string{"example.org"},
			},
			routegroup: &rgv1.RouteGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name: testStackSet.Name,
				},
			},
			updated: nil,
			expected: &networking.Ingress{
				ObjectMeta: stacksetOwned(testStackSet),
				Spec: networking.IngressSpec{
					Rules: exampleRules,
				},
			},
		},
		{
			name: "ingress is not removed if RouteGroup has an invalid updatedTimestamp",
			existing: &networking.Ingress{
				ObjectMeta: stacksetOwned(testStackSet),
				Spec: networking.IngressSpec{
					Rules: exampleRules,
				},
			},
			routegroupSpec: &zv1.RouteGroupSpec{
				Hosts: []string{"example.org"},
			},
			routegroup: &rgv1.RouteGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name: testStackSet.Name,
					Annotations: map[string]string{
						core.StacksetControllerUpdateTimestampAnnotationkey: "ANotValidTimeStamp",
					},
				},
			},
			updated: nil,
			expected: &networking.Ingress{
				ObjectMeta: stacksetOwned(testStackSet),
				Spec: networking.IngressSpec{
					Rules: exampleRules,
				},
			},
		},
		{
			name: "ingress is not removed if RouteGroup is not yet created",
			existing: &networking.Ingress{
				ObjectMeta: stacksetOwned(testStackSet),
				Spec: networking.IngressSpec{
					Rules: exampleRules,
				},
			},
			routegroupSpec: &zv1.RouteGroupSpec{
				Hosts: []string{"example.org"},
			},
			updated: nil,
			expected: &networking.Ingress{
				ObjectMeta: stacksetOwned(testStackSet),
				Spec: networking.IngressSpec{
					Rules: exampleRules,
				},
			},
		},
		{
			name: "ingress is removed if RouteGroup is old enough",
			existing: &networking.Ingress{
				ObjectMeta: stacksetOwned(testStackSet),
				Spec: networking.IngressSpec{
					Rules: exampleRules,
				},
			},
			routegroupSpec: &zv1.RouteGroupSpec{
				Hosts: []string{"example.org"},
			},
			routegroup: &rgv1.RouteGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name: testStackSet.Name,
					// TODO: Stop using this magic number (-2)
					Annotations: map[string]string{
						core.StacksetControllerUpdateTimestampAnnotationkey: time.Now().UTC().Add(-2 * time.Minute).Format(time.RFC3339),
					},
				},
			},
			updated:  nil,
			expected: nil,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := NewTestEnvironment()

			stackset := testStackSet
			if tc.routegroupSpec != nil {
				stackset.Spec.RouteGroup = tc.routegroupSpec
			}

			err := env.CreateStacksets(context.Background(), []zv1.StackSet{stackset})
			require.NoError(t, err)

			if tc.existing != nil {
				err = env.CreateIngresses(context.Background(), []networking.Ingress{*tc.existing})
				require.NoError(t, err)
			}

			err = env.controller.ReconcileStackSetIngress(context.Background(), &stackset, tc.existing, tc.routegroup, func() (*networking.Ingress, error) {
				return tc.updated, nil
			})
			require.NoError(t, err)

			updated, err := env.client.NetworkingV1beta1().Ingresses(stackset.Namespace).Get(context.Background(), stackset.Name, metav1.GetOptions{})
			if tc.expected != nil {
				require.NoError(t, err)
				require.Equal(t, tc.expected, updated)
			} else {
				require.True(t, errors.IsNotFound(err))
			}
		})
	}
}

func TestReconcileStackSetRouteGroup(t *testing.T) {
	exampleSpec := rgv1.RouteGroupSpec{
		Hosts: []string{"example.org"},
		Backends: []rgv1.RouteGroupBackend{
			{
				Name:        "foo",
				Type:        rgv1.ServiceRouteGroupBackend,
				ServiceName: "foo",
				ServicePort: 80,
			},
		},
		DefaultBackends: []rgv1.RouteGroupBackendReference{
			{
				BackendName: "foo",
				Weight:      100,
			},
		},
		Routes: []rgv1.RouteGroupRouteSpec{
			{
				PathSubtree: "/",
			},
		},
	}

	exampleUpdatedSpec := rgv1.RouteGroupSpec{
		Hosts: []string{"example.org"},
		Backends: []rgv1.RouteGroupBackend{
			{
				Name:        "foo",
				Type:        rgv1.ServiceRouteGroupBackend,
				ServiceName: "foo",
				ServicePort: 80,
			},
			{
				Name:    "remote",
				Type:    rgv1.NetworkRouteGroupBackend,
				Address: "https://zalando.de",
			},
		},
		DefaultBackends: []rgv1.RouteGroupBackendReference{
			{
				BackendName: "foo",
				Weight:      100,
			},
		},
		Routes: []rgv1.RouteGroupRouteSpec{
			{
				PathSubtree: "/",
			},
			{
				PathSubtree: "/redirect",
				Backends: []rgv1.RouteGroupBackendReference{
					{
						BackendName: "remote",
						Weight:      100,
					},
				},
			},
		},
	}

	for _, tc := range []struct {
		name        string
		existing    *rgv1.RouteGroup
		ingress     *networking.Ingress
		ingressSpec *zv1.StackSetIngressSpec
		updated     *rgv1.RouteGroup
		expected    *rgv1.RouteGroup
	}{
		{
			name: "routegroup is created if it doesn't exist",
			updated: &rgv1.RouteGroup{
				ObjectMeta: stacksetOwned(testStackSet),
				Spec:       exampleSpec,
			},
			expected: &rgv1.RouteGroup{
				ObjectMeta: stacksetOwned(testStackSet),
				Spec:       exampleSpec,
			},
		},
		{
			name: "routegroup is removed if it is no longer needed",
			existing: &rgv1.RouteGroup{
				ObjectMeta: stacksetOwned(testStackSet),
				Spec:       exampleSpec,
			},
			updated:  nil,
			expected: nil,
		},
		{
			name: "routegroup is updated if the spec is changed",
			existing: &rgv1.RouteGroup{
				ObjectMeta: stacksetOwned(testStackSet),
				Spec:       exampleSpec,
			},
			updated: &rgv1.RouteGroup{
				ObjectMeta: stacksetOwned(testStackSet),
				Spec:       exampleUpdatedSpec,
			},
			expected: &rgv1.RouteGroup{
				ObjectMeta: stacksetOwned(testStackSet),
				Spec:       exampleUpdatedSpec,
			},
		},
		{
			// TODO: Take into account Ingress UpdatedTimestamp
			name: "routegroup is not removed if Ingress is too young",
			existing: &rgv1.RouteGroup{
				ObjectMeta: stacksetOwned(testStackSet),
				Spec:       exampleSpec,
			},
			ingressSpec: &zv1.StackSetIngressSpec{
				Hosts: []string{"example.org"},
			},
			ingress: &networking.Ingress{
				ObjectMeta: metav1.ObjectMeta{
					Name: testStackSet.Name,
					Annotations: map[string]string{
						core.StacksetControllerUpdateTimestampAnnotationkey: time.Now().Format(time.RFC3339),
					},
				},
			},
			updated: nil,
			expected: &rgv1.RouteGroup{
				ObjectMeta: stacksetOwned(testStackSet),
				Spec:       exampleSpec,
			},
		},
		{
			name: "routegroup is not removed if Ingress does not have the updatedTimestamp",
			existing: &rgv1.RouteGroup{
				ObjectMeta: stacksetOwned(testStackSet),
				Spec:       exampleSpec,
			},
			ingressSpec: &zv1.StackSetIngressSpec{
				Hosts: []string{"example.org"},
			},
			ingress: &networking.Ingress{
				ObjectMeta: metav1.ObjectMeta{
					Name: testStackSet.Name,
				},
			},
			updated: nil,
			expected: &rgv1.RouteGroup{
				ObjectMeta: stacksetOwned(testStackSet),
				Spec:       exampleSpec,
			},
		},
		{
			name: "routegroup is not removed if Ingress has an invalid updatedTimestamp",
			existing: &rgv1.RouteGroup{
				ObjectMeta: stacksetOwned(testStackSet),
				Spec:       exampleSpec,
			},
			ingressSpec: &zv1.StackSetIngressSpec{
				Hosts: []string{"example.org"},
			},
			ingress: &networking.Ingress{
				ObjectMeta: metav1.ObjectMeta{
					Name: testStackSet.Name,
					Annotations: map[string]string{
						core.StacksetControllerUpdateTimestampAnnotationkey: "ANotValidTimeStamp",
					},
				},
			},
			updated: nil,
			expected: &rgv1.RouteGroup{
				ObjectMeta: stacksetOwned(testStackSet),
				Spec:       exampleSpec,
			},
		},
		{
			name: "routegroup is not removed if Ingress is not yet created",
			existing: &rgv1.RouteGroup{
				ObjectMeta: stacksetOwned(testStackSet),
				Spec:       exampleSpec,
			},
			ingressSpec: &zv1.StackSetIngressSpec{
				Hosts: []string{"example.org"},
			},
			updated: nil,
			expected: &rgv1.RouteGroup{
				ObjectMeta: stacksetOwned(testStackSet),
				Spec:       exampleSpec,
			},
		},
		{
			// TODO: Take into account Ingress UpdatedTimestamp
			name: "routegroup is removed if Ingress is old enough",
			existing: &rgv1.RouteGroup{
				ObjectMeta: stacksetOwned(testStackSet),
				Spec:       exampleSpec,
			},
			ingressSpec: &zv1.StackSetIngressSpec{
				Hosts: []string{"example.org"},
			},
			ingress: &networking.Ingress{
				ObjectMeta: metav1.ObjectMeta{
					Name: testStackSet.Name,
					// TODO: Stop using this magic number (-2)
					Annotations: map[string]string{
						core.StacksetControllerUpdateTimestampAnnotationkey: time.Now().UTC().Add(-2 * time.Minute).Format(time.RFC3339),
					},
				},
			},
			updated:  nil,
			expected: nil,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := NewTestEnvironment()

			stackset := testStackSet
			if tc.ingressSpec != nil {
				stackset.Spec.Ingress = tc.ingressSpec
			}

			err := env.CreateStacksets(context.Background(), []zv1.StackSet{stackset})
			require.NoError(t, err)

			if tc.existing != nil {
				err = env.CreateRouteGroups(context.Background(), []rgv1.RouteGroup{*tc.existing})
				require.NoError(t, err)
			}

			err = env.controller.ReconcileStackSetRouteGroup(context.Background(), &stackset, tc.existing, tc.ingress, func() (*rgv1.RouteGroup, error) {
				return tc.updated, nil
			})
			require.NoError(t, err)

			updated, err := env.client.RouteGroupV1().RouteGroups(stackset.Namespace).Get(context.Background(), stackset.Name, metav1.GetOptions{})
			if tc.expected != nil {
				require.NoError(t, err)
				require.Equal(t, tc.expected, updated)
			} else {
				require.True(t, errors.IsNotFound(err))
			}
		})
	}
}
