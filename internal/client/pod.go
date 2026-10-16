// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s

package client

import (
	"fmt"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
)

// ToPod converts a cached pod into a typed pod.
func ToPod(o runtime.Object) (*v1.Pod, error) {
	switch t := o.(type) {
	case *v1.Pod:
		return t, nil
	case *unstructured.Unstructured:
		var pod v1.Pod
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(t.Object, &pod); err != nil {
			return nil, err
		}
		return &pod, nil
	default:
		return nil, fmt.Errorf("expected a pod but got %T", o)
	}
}
