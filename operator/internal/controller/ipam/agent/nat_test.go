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

package agent

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

const natDump = `-P POSTROUTING ACCEPT
-A POSTROUTING -m comment --comment "kubernetes postrouting rules" -j KUBE-POSTROUTING
-A POSTROUTING -s 10.244.0.5/32 -m comment --comment "name: \"podman\" id: \"abc\"" -j CNI-1111
-A POSTROUTING -s 10.244.0.9/32 -m comment --comment "x" -j CNI-2222
-A POSTROUTING -s 10.9.0.5/32 -m comment --comment "other network" -j CNI-3333
-A POSTROUTING -s 10.244.0.6/32 -j ACCEPT
-A POSTROUTING -s 172.17.0.0/16 ! -o docker0 -j MASQUERADE`

func TestStaleCNIMasqueradeOnlyMatchesOurAddresses(t *testing.T) {
	specs, chains := StaleCNIMasquerade(natDump, []string{block0})
	if !reflect.DeepEqual(chains, []string{"CNI-1111", "CNI-2222"}) {
		t.Fatalf("chains = %v", chains)
	}
	if len(specs) != 2 || specs[0][0] != "-s" || specs[0][1] != "10.244.0.5/32" {
		t.Fatalf("specs = %v", specs)
	}
	// The comment is one argument, quotes removed.
	if want := []string{"-s", "10.244.0.5/32", "-m", "comment", "--comment", `name: "podman" id: "abc"`, "-j", "CNI-1111"}; !reflect.DeepEqual(specs[0], want) {
		t.Fatalf("spec = %q, want %q", specs[0], want)
	}
	if s, _ := StaleCNIMasquerade(natDump, nil); len(s) != 0 {
		t.Fatalf("no blocks, no rules: %v", s)
	}
}

func TestCleanStaleNAT(t *testing.T) {
	var calls []string
	run := func(args ...string) ([]byte, error) {
		calls = append(calls, strings.Join(args, " "))
		if args[len(args)-1] == chainPost && args[2] == "-S" {
			return []byte(natDump), nil
		}
		return nil, nil
	}
	n, err := CleanStaleNAT(run, []string{block0})
	if err != nil || n != 2 {
		t.Fatalf("removed %d, err %v", n, err)
	}
	joined := strings.Join(calls, "\n")
	for _, want := range []string{"-t nat -D POSTROUTING -s 10.244.0.5/32", "-t nat -F CNI-1111", "-t nat -X CNI-2222"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing call %q in\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "CNI-3333") {
		t.Error("a rule outside our blocks was touched")
	}
}

func TestCleanStaleNATReportsFailures(t *testing.T) {
	run := func(args ...string) ([]byte, error) { return nil, errors.New("iptables: permission denied") }
	if _, err := CleanStaleNAT(run, []string{block0}); err == nil {
		t.Fatal("a failing iptables must be reported")
	}
}

func TestSyncCleansNATOncePerInterval(t *testing.T) {
	e := newEnv(t, nobj(mkNode("n1", "192.168.0.1", true)), obj(mkBlock("n1", block0, "Active")))
	listed := 0
	a := e.agent("n1")
	a.NATEvery = time.Minute
	a.NAT = func(args ...string) ([]byte, error) {
		if args[2] == "-S" {
			listed++
		}
		return nil, nil
	}
	ctx := t.Context()
	for range 3 {
		if err := a.Sync(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if listed != 1 {
		t.Fatalf("iptables listed %d times in 3 passes, want 1", listed)
	}
	e.now = e.now.Add(2 * time.Minute)
	_ = a.Sync(ctx)
	if listed != 2 {
		t.Fatalf("after the interval: %d, want 2", listed)
	}
}
