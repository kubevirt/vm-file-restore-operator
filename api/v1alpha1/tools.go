//go:build tools

package v1alpha1

import (
	_ "github.com/operator-framework/api/pkg/operators/v1alpha1"
	_ "kubevirt.io/controller-lifecycle-operator-sdk/api"
)
