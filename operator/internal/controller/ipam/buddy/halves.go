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

package buddy

import (
	"fmt"
	"net"
)

// Halves returns the two buddy halves of an aligned IPv4 block: the lower and
// the upper one. A /27 splits into two /28s.
func Halves(cidr string) (lower, upper string, err error) {
	_, n, err := net.ParseCIDR(cidr)
	if err != nil {
		return "", "", fmt.Errorf("invalid CIDR %q: %w", cidr, err)
	}
	ones, bits := n.Mask.Size()
	if bits != 32 || ones >= 32 {
		return "", "", fmt.Errorf("cannot split %s", cidr)
	}
	base := ipToUint32(n.IP)
	half := uint32(1) << (31 - ones)
	lower = fmt.Sprintf("%s/%d", uint32ToIP(base), ones+1)
	upper = fmt.Sprintf("%s/%d", uint32ToIP(base+half), ones+1)
	return lower, upper, nil
}

// OtherHalf returns the half of cidr that is not half. It fails when half is
// not one of the two halves of cidr.
func OtherHalf(cidr, half string) (string, error) {
	lower, upper, err := Halves(cidr)
	if err != nil {
		return "", err
	}
	switch half {
	case lower:
		return upper, nil
	case upper:
		return lower, nil
	}
	return "", fmt.Errorf("%s is not a half of %s", half, cidr)
}
