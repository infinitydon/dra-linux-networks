package driver

import (
	"testing"

	"k8s.io/apimachinery/pkg/types"
)

func TestDeviceStoreKeySeparatesSharedClaims(t *testing.T) {
	device := "enp8s20"
	first := deviceStoreKey(types.UID("claim-a"), device, nil)
	second := deviceStoreKey(types.UID("claim-b"), device, nil)
	if first == second {
		t.Fatalf("different claims produced the same store key %q", first)
	}
	shareA, shareB := "share-a", "share-b"
	first = deviceStoreKey(types.UID("claim-a"), device, &shareA)
	second = deviceStoreKey(types.UID("claim-a"), device, &shareB)
	if first == second {
		t.Fatalf("different shares produced the same store key %q", first)
	}
}
