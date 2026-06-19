package driver

import (
	"encoding/binary"
	"fmt"
	"net"

	"k8s.io/apimachinery/pkg/types"

	"github.com/infinitydon/dra-linux-networks/internal/config"
)

func (d *Driver) applyIPAM(netCfg *NetworkConfig, claimUID, podUID types.UID) error {
	if netCfg.IPPool == "" {
		return nil
	}
	pool, ok := d.ipPools[netCfg.IPPool]
	if !ok {
		return fmt.Errorf("unknown IP pool %q", netCfg.IPPool)
	}
	subnetIP, subnet, err := net.ParseCIDR(pool.Subnet)
	if err != nil {
		return fmt.Errorf("parse pool %s subnet %q: %w", pool.Name, pool.Subnet, err)
	}
	subnet.IP = subnetIP
	start := net.ParseIP(pool.RangeStart).To4()
	end := net.ParseIP(pool.RangeEnd).To4()
	if start == nil || end == nil {
		return fmt.Errorf("pool %s range must be IPv4", pool.Name)
	}
	if !subnet.Contains(start) || !subnet.Contains(end) {
		return fmt.Errorf("pool %s range %s-%s is outside subnet %s", pool.Name, pool.RangeStart, pool.RangeEnd, pool.Subnet)
	}
	startInt := ipToUint32(start)
	endInt := ipToUint32(end)
	if startInt > endInt {
		return fmt.Errorf("pool %s rangeStart must be <= rangeEnd", pool.Name)
	}

	address := netCfg.Address
	if allocation, ok := d.store.AllocationForClaim(claimUID); ok {
		address = allocation.Address
	}
	if address == "" {
		address, err = d.nextFreeAddress(pool, subnet, startInt, endInt)
		if err != nil {
			return err
		}
	} else {
		address, err = normalizeRequestedAddress(pool, subnet, address)
		if err != nil {
			return err
		}
		ip, _, _ := net.ParseCIDR(address)
		ipInt := ipToUint32(ip.To4())
		if ipInt < startInt || ipInt > endInt {
			return fmt.Errorf("requested address %s is outside pool %s range %s-%s", address, pool.Name, pool.RangeStart, pool.RangeEnd)
		}
	}

	if err := d.store.ReserveIP(pool.Name, address, claimUID, podUID); err != nil {
		return fmt.Errorf("reserve %s from pool %s: %w", address, pool.Name, err)
	}
	netCfg.Address = address
	netCfg.Addresses = []string{address}
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

func (d *Driver) nextFreeAddress(pool config.IPPool, subnet *net.IPNet, start, end uint32) (string, error) {
	ones, _ := subnet.Mask.Size()
	for current := start; current <= end; current++ {
		ip := uint32ToIP(current)
		address := fmt.Sprintf("%s/%d", ip.String(), ones)
		if !d.store.IsIPAllocated(pool.Name, address) {
			return address, nil
		}
		if current == ^uint32(0) {
			break
		}
	}
	return "", fmt.Errorf("pool %s has no free addresses", pool.Name)
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
