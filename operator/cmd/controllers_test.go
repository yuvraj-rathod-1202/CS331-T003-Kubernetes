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

package main

import "testing"

func TestParseControllers(t *testing.T) {
	all, err := parseControllers("all")
	if err != nil || len(all) != 3 {
		t.Fatalf("all: %v %v", all, err)
	}
	one, err := parseControllers(" ipam ")
	if err != nil || len(one) != 1 || !one["ipam"] {
		t.Fatalf("ipam: %v %v", one, err)
	}
	two, err := parseControllers("ipam,cniresilience")
	if err != nil || !two["ipam"] || !two["cniresilience"] || two["networkremediation"] {
		t.Fatalf("two: %v %v", two, err)
	}
	for _, bad := range []string{"", "nope", "ipam,nope"} {
		if _, err := parseControllers(bad); err == nil {
			t.Errorf("%q should be rejected", bad)
		}
	}
}
