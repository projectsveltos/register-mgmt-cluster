/*
Copyright 2024. projectsveltos.io. All rights reserved.

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

	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
)

var _ = Describe("SveltosCluster for management cluster", func() {
	It("Verifies management cluster is registered", Label("FV", "EXTENDED"), func() {
		By("Verify SveltosCluster for management cluster has been created")
		Eventually(func() bool {
			sveltosClusters := libsveltosv1beta1.SveltosClusterList{}
			err := k8sClient.List(context.TODO(), &sveltosClusters)
			if err != nil {
				return false
			}
			return len(sveltosClusters.Items) > 0
		}, timeout, pollingInterval).Should(BeTrue())

		// Verifying SveltosCluster is ready means validating Kubeconfig
		By("Verify SveltosCluster for management cluster is ready")
		Eventually(func() bool {
			sveltosClusters := libsveltosv1beta1.SveltosClusterList{}
			err := k8sClient.List(context.TODO(), &sveltosClusters)
			if err != nil {
				return false
			}
			if len(sveltosClusters.Items) == 0 {
				return false
			}
			for i := range sveltosClusters.Items {
				sv := &sveltosClusters.Items[i]
				if !sv.Status.Ready {
					return false
				}
			}
			return true
		}, timeout, pollingInterval).Should(BeTrue())
	})
})
