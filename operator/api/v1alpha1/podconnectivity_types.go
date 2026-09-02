/*
Copyright 2026.

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

package v1alpha1

// PodConnectivitySpec configures the pod-to-pod connectivity probing module.
//
// TODO: Team member implementing the PodConnectivity module should add probe intervals
type PodConnectivitySpec struct {
	// Enabled toggles the pod connectivity monitoring module.
	// +kubebuilder:default=true
	Enabled bool `json:"enabled"`
}
