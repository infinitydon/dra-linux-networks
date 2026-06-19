package driver

import (
	"context"
	"encoding/binary"
	"fmt"
	"net"

	corev1 "k8s.io/api/core/v1"
	resourceapi "k8s.io/api/resource/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/infinitydon/dra-linux-networks/internal/config"
	"github.com/infinitydon/dra-linux-networks/internal/ipamapi"
)

func (d *Driver) applyIPAM(ctx context.Context, netCfg *NetworkConfig, claim *resourceapi.ResourceClaim, pod *corev1.Pod) error {
	if netCfg.IPPool == "" {
		return nil
	}
	d.reloadIPPools(ctx)
	pool, ok := d.ipPools[netCfg.IPPool]
	if !ok {
		return fmt.Errorf("unknown IP pool %q", netCfg.IPPool)
	}
	subnetIP, subnet, err := net.ParseCIDR(pool.Subnet)
	if err != nil {
		return fmt.Errorf("parse pool %s subnet %q: %w", pool.Name, pool.Subnet, err)
	}
	subnet.IP = subnetIP
	if err := validateIPRanges(pool.Name, subnet, "allocation", pool.Allocations); err != nil {
		return err
	}
	if err := validateIPRanges(pool.Name, subnet, "reservation", pool.Reservations); err != nil {
		return err
	}

	address := netCfg.Address
	if allocation, ok, err := ipamapi.FindForClaim(ctx, d.dynamic, claim.UID); err != nil {
		return fmt.Errorf("find allocation for claim %s/%s: %w", claim.Namespace, claim.Name, err)
	} else if ok {
		if allocation.Pool != pool.Name {
			return fmt.Errorf("claim %s/%s already has an allocation from pool %s", claim.Namespace, claim.Name, allocation.Pool)
		}
		address = allocation.Address
		if err := d.store.ReserveIP(pool.Name, address, claim.UID, pod.UID); err != nil {
			return err
		}
		netCfg.Address = address
		netCfg.Addresses = []string{address}
		return applyPoolRoutes(netCfg, pool)
	}
	if address == "" {
		address, err = d.reserveDynamicAddress(ctx, pool, subnet, claim, pod)
		if err != nil {
			return err
		}
	} else {
		address, err = normalizeRequestedAddress(pool, subnet, address)
		if err != nil {
			return err
		}
		ip, _, _ := net.ParseCIDR(address)
		if !ipInRanges(ip.To4(), pool.Reservations) {
			return fmt.Errorf("requested static address %s is not reserved in pool %s", address, pool.Name)
		}
		if err := d.reserveClusterAddress(ctx, pool.Name, address, "Static", claim, pod); err != nil {
			if apierrors.IsAlreadyExists(err) {
				return fmt.Errorf("static address %s is already allocated", address)
			}
			return fmt.Errorf("reserve static address %s: %w", address, err)
		}
	}

	if err := d.store.ReserveIP(pool.Name, address, claim.UID, pod.UID); err != nil {
		return fmt.Errorf("reserve %s from pool %s: %w", address, pool.Name, err)
	}
	netCfg.Address = address
	netCfg.Addresses = []string{address}
	return applyPoolRoutes(netCfg, pool)
}

func applyPoolRoutes(netCfg *NetworkConfig, pool config.IPPool) error {
	if len(netCfg.Routes) == 0 {
		for _, route := range pool.Routes {
			netCfg.Routes = append(netCfg.Routes, Route{
				Destination: route.Destination,
				Gateway:     route.Gateway,
			})
		}
	}
	return nil
}

func (d *Driver) reserveDynamicAddress(ctx context.Context, pool config.IPPool, subnet *net.IPNet, claim *resourceapi.ResourceClaim, pod *corev1.Pod) (string, error) {
	ones, _ := subnet.Mask.Size()
	for _, allocationRange := range pool.Allocations {
		for _, ip := range expandIPRange(allocationRange) {
			if ipInRanges(ip, pool.Reservations) {
				continue
			}
			address := fmt.Sprintf("%s/%d", ip.String(), ones)
			err := d.reserveClusterAddress(ctx, pool.Name, address, "Dynamic", claim, pod)
			if err == nil {
				return address, nil
			}
			if !apierrors.IsAlreadyExists(err) {
				return "", fmt.Errorf("reserve dynamic address %s: %w", address, err)
			}
			if ipToUint32(ip) == ^uint32(0) {
				break
			}
		}
	}
	return "", fmt.Errorf("pool %s has no free addresses", pool.Name)
}

func (d *Driver) reserveClusterAddress(ctx context.Context, pool, address, allocationType string, claim *resourceapi.ResourceClaim, pod *corev1.Pod) error {
	_, err := ipamapi.Reserve(ctx, d.dynamic, ipamapi.Allocation{
		Pool:           pool,
		Address:        address,
		AllocationType: allocationType,
		Claim: ipamapi.ObjectReference{
			Namespace: claim.Namespace,
			Name:      claim.Name,
			UID:       claim.UID,
		},
		Pod: ipamapi.ObjectReference{
			Namespace: pod.Namespace,
			Name:      pod.Name,
			UID:       pod.UID,
		},
		NodeName: d.nodeName,
	})
	return err
}

func validateIPRanges(poolName string, subnet *net.IPNet, kind string, ranges []config.IPRange) error {
	for i, ipRange := range ranges {
		for _, address := range ipRange.Addresses {
			ip := net.ParseIP(address).To4()
			if ip == nil {
				return fmt.Errorf("pool %s %s[%d] address %s must be IPv4", poolName, kind, i, address)
			}
			if !subnet.Contains(ip) {
				return fmt.Errorf("pool %s %s[%d] address %s is outside subnet %s", poolName, kind, i, address, subnet.String())
			}
		}
		if ipRange.RangeStart == "" && ipRange.RangeEnd == "" {
			continue
		}
		start := net.ParseIP(ipRange.RangeStart).To4()
		end := net.ParseIP(ipRange.RangeEnd).To4()
		if start == nil || end == nil {
			return fmt.Errorf("pool %s %s[%d] range must be IPv4", poolName, kind, i)
		}
		if !subnet.Contains(start) || !subnet.Contains(end) {
			return fmt.Errorf("pool %s %s[%d] range %s-%s is outside subnet %s", poolName, kind, i, ipRange.RangeStart, ipRange.RangeEnd, subnet.String())
		}
		if ipToUint32(start) > ipToUint32(end) {
			return fmt.Errorf("pool %s %s[%d] rangeStart must be <= rangeEnd", poolName, kind, i)
		}
	}
	return nil
}

func ipInRanges(ip net.IP, ranges []config.IPRange) bool {
	value := ipToUint32(ip)
	for _, ipRange := range ranges {
		for _, address := range ipRange.Addresses {
			if parsed := net.ParseIP(address).To4(); parsed != nil && ipToUint32(parsed) == value {
				return true
			}
		}
		if ipRange.RangeStart == "" || ipRange.RangeEnd == "" {
			continue
		}
		start := net.ParseIP(ipRange.RangeStart).To4()
		end := net.ParseIP(ipRange.RangeEnd).To4()
		if start == nil || end == nil {
			continue
		}
		if value >= ipToUint32(start) && value <= ipToUint32(end) {
			return true
		}
	}
	return false
}

func expandIPRange(ipRange config.IPRange) []net.IP {
	ips := []net.IP{}
	for _, address := range ipRange.Addresses {
		if ip := net.ParseIP(address).To4(); ip != nil {
			ips = append(ips, ip)
		}
	}
	if ipRange.RangeStart == "" || ipRange.RangeEnd == "" {
		return ips
	}
	start := net.ParseIP(ipRange.RangeStart).To4()
	end := net.ParseIP(ipRange.RangeEnd).To4()
	if start == nil || end == nil {
		return ips
	}
	for current, last := ipToUint32(start), ipToUint32(end); current <= last; current++ {
		ips = append(ips, uint32ToIP(current))
		if current == ^uint32(0) {
			break
		}
	}
	return ips
}

func normalizeRequestedAddress(pool config.IPPool, subnet *net.IPNet, address string) (string, error) {
	ip, ipNet, err := net.ParseCIDR(address)
	if err != nil {
		return "", fmt.Errorf("parse requested address %q: %w", address, err)
	}
	ip4 := ip.To4()
	if ip4 == nil {
		return "", fmt.Errorf("requested address %s must be IPv4", address)
	}
	if !subnet.Contains(ip4) {
		return "", fmt.Errorf("requested address %s is outside pool %s subnet %s", address, pool.Name, pool.Subnet)
	}
	poolOnes, _ := subnet.Mask.Size()
	requestOnes, _ := ipNet.Mask.Size()
	if poolOnes != requestOnes {
		return "", fmt.Errorf("requested address %s prefix must match pool %s subnet %s", address, pool.Name, pool.Subnet)
	}
	return fmt.Sprintf("%s/%d", ip4.String(), poolOnes), nil
}

func ipToUint32(ip net.IP) uint32 {
	return binary.BigEndian.Uint32(ip.To4())
}

func uint32ToIP(value uint32) net.IP {
	ip := make(net.IP, 4)
	binary.BigEndian.PutUint32(ip, value)
	return ip
}
