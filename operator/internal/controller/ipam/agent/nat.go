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
	"fmt"
	"net"
	"os/exec"
	"strings"
)

const chainPost = "POSTROUTING"

// Runner runs a command on the host and returns its combined output.
type Runner func(args ...string) ([]byte, error)

// HostIPTables runs the node's own iptables, by entering PID 1's mount
// namespace, so the agent never has to match the host's iptables backend
// (legacy or nft). The pod needs hostPID and privileges.
func HostIPTables(args ...string) ([]byte, error) {
	full := append([]string{"-t", "1", "-m", "--", "iptables", "-w", "5"}, args...)
	return exec.Command("nsenter", full...).CombinedOutput()
}

// StaleCNIMasquerade finds nat POSTROUTING rules that jump to a CNI-* chain for
// a source address inside cidrs. Our config never masquerades (ptp ipMasq is
// off), so such a rule is a leftover of an earlier CNI plugin; it NATs any pod
// that is later given the same address (Phase 0, "stale NAT"). It returns the
// rule specification (for -D) and the chain.
func StaleCNIMasquerade(saveOutput string, cidrs []string) (specs [][]string, chains []string) {
	nets := make([]*net.IPNet, 0, len(cidrs))
	for _, c := range cidrs {
		if _, n, err := net.ParseCIDR(c); err == nil {
			nets = append(nets, n)
		}
	}
	for line := range strings.SplitSeq(saveOutput, "\n") {
		f := splitShell(strings.TrimSpace(line))
		if len(f) < 4 || f[0] != "-A" || f[1] != chainPost {
			continue
		}
		var src, target string
		for i := 2; i+1 < len(f); i++ {
			switch f[i] {
			case "-s":
				src = f[i+1]
			case "-j":
				target = f[i+1]
			}
		}
		if !strings.HasPrefix(target, "CNI-") || src == "" {
			continue
		}
		ip, _, err := net.ParseCIDR(src)
		if err != nil {
			ip = net.ParseIP(src)
		}
		for _, n := range nets {
			if ip != nil && n.Contains(ip) {
				specs = append(specs, f[2:])
				chains = append(chains, target)
				break
			}
		}
	}
	return specs, chains
}

// splitShell splits a line of iptables-save style output, honouring the double
// quotes it puts around comments and the backslash escapes inside them.
func splitShell(s string) []string {
	var out []string
	var cur strings.Builder
	quoted, have, esc := false, false, false
	for _, r := range s {
		switch {
		case esc:
			cur.WriteRune(r)
			esc = false
		case r == '\\':
			esc, have = true, true
		case r == '"':
			quoted, have = !quoted, true
		case r == ' ' && !quoted:
			if have {
				out = append(out, cur.String())
				cur.Reset()
				have = false
			}
		default:
			cur.WriteRune(r)
			have = true
		}
	}
	if have {
		out = append(out, cur.String())
	}
	return out
}

// CleanStaleNAT deletes the rules StaleCNIMasquerade finds, then their now
// unreferenced chains. It returns how many rules it removed.
func CleanStaleNAT(run Runner, cidrs []string) (int, error) {
	out, err := run("-t", "nat", "-S", chainPost)
	if err != nil {
		return 0, fmt.Errorf("list nat rules: %w: %s", err, strings.TrimSpace(string(out)))
	}
	specs, chains := StaleCNIMasquerade(string(out), cidrs)
	removed := 0
	var firstErr error
	seen := map[string]bool{}
	for i, spec := range specs {
		if out, err := run(append([]string{"-t", "nat", "-D", chainPost}, spec...)...); err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("delete rule %v: %w: %s", spec, err, strings.TrimSpace(string(out)))
			}
			continue
		}
		removed++
		if seen[chains[i]] {
			continue
		}
		seen[chains[i]] = true
		// The chain may still be referenced by another rule; then -X fails and
		// the next pass retries once that rule is gone.
		_, _ = run("-t", "nat", "-F", chains[i])
		_, _ = run("-t", "nat", "-X", chains[i])
	}
	return removed, firstErr
}
