/*
Copyright 2025. projectsveltos.io. All rights reserved.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package fv_test

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/types"

	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
)

var _ = Describe("Sveltos default instances", func() {
	It("Verifies default classifier and debuggingConfiguration instances", Label("FV", "EXTENDED"), func() {
		By("Verify default classifier instance is created")
		Eventually(func() bool {
			classifierInstance := libsveltosv1beta1.Classifier{}
			err := k8sClient.Get(context.TODO(),
				types.NamespacedName{Name: "default-classifier"},
				&classifierInstance)
			return err == nil
		}, timeout, pollingInterval).Should(BeTrue())

		By("Verify default debuggingConfiguration instance is created")
		Eventually(func() bool {
			dcInstance := libsveltosv1beta1.DebuggingConfiguration{}
			err := k8sClient.Get(context.TODO(),
				types.NamespacedName{Name: "default"},
				&dcInstance)
			return err == nil
		}, timeout, pollingInterval).Should(BeTrue())
	})
})
