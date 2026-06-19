package driver

import (
	"strings"
	"testing"
)

func TestPodStaticAddressRequiresMatchingPoolReference(t *testing.T) {
	base := AttrPrefix + "/net1"
	tests := []struct {
		name         string
		annotations  map[string]string
		expectedPool string
		wantAddress  string
		wantError    string
	}{
		{
			name: "matching pool",
			annotations: map[string]string{
				base + ".ip-pool": "lan-88",
				base + ".address": "192.168.88.10/24",
			},
			expectedPool: "lan-88",
			wantAddress:  "192.168.88.10/24",
		},
		{
			name: "missing pool annotation",
			annotations: map[string]string{
				base + ".address": "192.168.88.10/24",
			},
			expectedPool: "lan-88",
			wantError:    "must be set together",
		},
		{
			name: "mismatched pool",
			annotations: map[string]string{
				base + ".ip-pool": "other-pool",
				base + ".address": "192.168.88.10/24",
			},
			expectedPool: "lan-88",
			wantError:    "does not match",
		},
		{
			name: "claim has no pool",
			annotations: map[string]string{
				base + ".ip-pool": "lan-88",
				base + ".address": "192.168.88.10/24",
			},
			wantError: "requires ipPool",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			address, err := podStaticAddress(test.annotations, "net1", "net1", test.expectedPool)
			if test.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantError) {
					t.Fatalf("error = %v, want substring %q", err, test.wantError)
				}
				return
			}
			if err != nil || address != test.wantAddress {
				t.Fatalf("address = %q, err = %v; want %q", address, err, test.wantAddress)
			}
		})
	}
}
