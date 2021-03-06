package traffic

import (
	"context"
	"encoding/json"
	"fmt"

	zv1 "github.com/zalando-incubator/stackset-controller/pkg/apis/zalando.org/v1"
	"github.com/zalando-incubator/stackset-controller/pkg/clientset"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
)

const (
	stacksetHeritageLabelKey           = "stackset"
	StackTrafficWeightsAnnotationKey   = "zalando.org/stack-traffic-weights"
	DefaultBackendWeightsAnnotationKey = "zalando.org/backend-weights"
)

// Switcher is able to switch traffic between stacks.
type Switcher struct {
	client                      clientset.Interface
	backendWeightsAnnotationKey string
}

// NewSwitcher initializes a new traffic switcher.
func NewSwitcher(client clientset.Interface, backendWeightsAnnotationKey string) *Switcher {
	return &Switcher{
		client:                      client,
		backendWeightsAnnotationKey: backendWeightsAnnotationKey,
	}
}

// Switch changes traffic weight for a stack.
func (t *Switcher) Switch(ctx context.Context, stackset, stack, namespace string, weight float64) ([]StackTrafficWeight, error) {
	stacks, err := t.getStacks(ctx, stackset, namespace)
	if err != nil {
		return nil, err
	}

	normalized := normalizeWeights(stacks)
	newWeights, err := setWeightForStacks(normalized, stack, weight)
	if err != nil {
		return nil, err
	}

	changeNeeded := false
	stackWeights := make(map[string]float64, len(newWeights))
	for i, stack := range newWeights {
		if stack.Weight != stacks[i].Weight {
			changeNeeded = true
		}
		stackWeights[stack.Name] = stack.Weight
	}

	if changeNeeded {
		stackWeightsData, err := json.Marshal(&stackWeights)
		if err != nil {
			return nil, err
		}

		annotation := map[string]map[string]map[string]string{
			"metadata": map[string]map[string]string{
				"annotations": map[string]string{
					StackTrafficWeightsAnnotationKey: string(stackWeightsData),
				},
			},
		}

		annotationData, err := json.Marshal(&annotation)
		if err != nil {
			return nil, err
		}

		_, err = t.client.NetworkingV1().Ingresses(namespace).Patch(ctx, stackset, types.StrategicMergePatchType, annotationData, metav1.PatchOptions{})
		if err != nil {
			return nil, err
		}
	}

	return newWeights, nil
}

type StackTrafficWeight struct {
	Name         string
	Weight       float64
	ActualWeight float64
}

// TrafficWeights returns a list of stacks with their current traffic weight.
func (t *Switcher) TrafficWeights(ctx context.Context, stackset, namespace string) ([]StackTrafficWeight, error) {
	stacks, err := t.getStacks(ctx, stackset, namespace)
	if err != nil {
		return nil, err
	}
	return normalizeWeights(stacks), nil
}

// getStacks returns the stacks of the stackset.
func (t *Switcher) getStacks(ctx context.Context, stackset, namespace string) ([]StackTrafficWeight, error) {
	heritageLabels := map[string]string{
		stacksetHeritageLabelKey: stackset,
	}
	opts := metav1.ListOptions{
		LabelSelector: labels.Set(heritageLabels).String(),
	}

	stacks, err := t.client.ZalandoV1().Stacks(namespace).List(ctx, opts)
	if err != nil {
		return nil, fmt.Errorf("failed to list stacks of stackset %s/%s: %v", namespace, stackset, err)
	}

	desired, actual, err := t.getIngressTraffic(ctx, stackset, namespace, stacks.Items)
	if err != nil {
		return nil, fmt.Errorf("failed to get Ingress traffic for StackSet %s/%s: %v", namespace, stackset, err)
	}

	stackWeights := make([]StackTrafficWeight, 0, len(stacks.Items))
	for _, stack := range stacks.Items {
		stackWeight := StackTrafficWeight{
			Name:         stack.Name,
			Weight:       desired[stack.Name],
			ActualWeight: actual[stack.Name],
		}

		stackWeights = append(stackWeights, stackWeight)
	}
	return stackWeights, nil
}

func (t *Switcher) getIngressTraffic(ctx context.Context, name, namespace string, stacks []zv1.Stack) (map[string]float64, map[string]float64, error) {
	if len(stacks) == 0 {
		return map[string]float64{}, map[string]float64{}, nil
	}

	ingress, err := t.client.NetworkingV1().Ingresses(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, nil, err
	}

	desiredTraffic := make(map[string]float64, len(stacks))
	if weights, ok := ingress.Annotations[StackTrafficWeightsAnnotationKey]; ok {
		err := json.Unmarshal([]byte(weights), &desiredTraffic)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to get current desired Stack traffic weights: %v", err)
		}
	}

	actualTraffic := make(map[string]float64, len(stacks))
	if weights, ok := ingress.Annotations[t.backendWeightsAnnotationKey]; ok {
		err := json.Unmarshal([]byte(weights), &actualTraffic)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to get current actual Stack traffic weights: %v", err)
		}
	}

	return desiredTraffic, actualTraffic, nil
}

// setWeightForStacks sets new traffic weight for the specified stack and adjusts
// the other stack weights relatively.
// It's assumed that the sum of weights over all stacks are 100.
func setWeightForStacks(stacks []StackTrafficWeight, stackName string, weight float64) ([]StackTrafficWeight, error) {
	newWeights := make([]StackTrafficWeight, len(stacks))
	currentWeight := float64(0)
	for i, stack := range stacks {
		if stack.Name == stackName {
			currentWeight = stack.Weight
			stack.Weight = weight
			newWeights[i] = stack
			break
		}
	}

	change := float64(0)

	if currentWeight < 100 {
		change = (100 - weight) / (100 - currentWeight)
	} else if weight < 100 {
		return nil, fmt.Errorf("'%s' is the only Stack getting traffic, Can't reduce it to %.1f%%", stackName, weight)
	}

	for i, stack := range stacks {
		if stack.Name != stackName {
			stack.Weight *= change
			newWeights[i] = stack
		}
	}

	return newWeights, nil
}

// allZero returns true if all weights defined in the map are 0.
func allZero(stacks []StackTrafficWeight) bool {
	for _, stack := range stacks {
		if stack.Weight > 0 {
			return false
		}
	}
	return true
}

// normalizeWeights normalizes the traffic weights specified on the stacks.
// If all weights are zero the total weight of 100 is distributed equally
// between all stacks.
// If not all weights are zero they are normalized to a sum of 100.
func normalizeWeights(stacks []StackTrafficWeight) []StackTrafficWeight {
	newWeights := make([]StackTrafficWeight, len(stacks))
	// if all weights are zero distribute them equally to all backends
	if allZero(stacks) && len(stacks) > 0 {
		eqWeight := 100 / float64(len(stacks))
		for i, stack := range stacks {
			stack.Weight = eqWeight
			newWeights[i] = stack
		}
		return newWeights
	}

	// if not all weights are zero, normalize them to a sum of 100
	sum := float64(0)
	for _, stack := range stacks {
		sum += stack.Weight
	}

	for i, stack := range stacks {
		stack.Weight = stack.Weight / sum * 100
		newWeights[i] = stack
	}

	return newWeights
}
