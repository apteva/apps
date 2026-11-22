package main

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// commonTCP is our hand-picked list of frequently exposed TCP services,
// ordered roughly by how often they show up on internet-facing hosts.
var commonTCP = []int{
	80, 443, 22, 21, 25, 3389, 110, 445, 139, 143, 53, 135, 3306, 8080, 1723, 111, 995, 993, 5900, 1025,
	587, 8888, 199, 1720, 465, 548, 113, 81, 6001, 10000, 514, 5060, 179, 1026, 2000, 8443, 8000, 32768, 554, 26,
	1433, 49152, 2001, 515, 8008, 49154, 1027, 5666, 646, 5000, 5631, 631, 49153, 8081, 2049, 88, 79, 5800, 106, 2121,
	1110, 49155, 6000, 513, 990, 5357, 427, 49156, 543, 544, 5101, 144, 7, 389, 8009, 3128, 444, 9999, 5009, 7070,
	5190, 3000, 5432, 1900, 3986, 13, 1029, 9, 5051, 6646, 49157, 1028, 873, 1755, 2717, 4899, 9100, 119, 37, 23,
}

// extraHighTCP adds well-known high ports for databases, admin panels,
// container and orchestration APIs that the standard profile should cover.
var extraHighTCP = []int{
	1521, 1883, 2375, 2376, 2379, 2380, 3001, 3260, 4369, 4443, 4444, 4848, 5044, 5222, 5601, 5672, 5984, 5985, 5986,
	6000, 6379, 6443, 6666, 7001, 7002, 7474, 7687, 8001, 8002, 8082, 8083, 8086, 8088, 8089, 8090, 8161, 8180, 8222,
	8333, 8500, 8545, 8686, 8834, 8880, 8983, 9000, 9001, 9042, 9043, 9060, 9080, 9090, 9091, 9092, 9093, 9200, 9300,
	9418, 9443, 9600, 10250, 10255, 11211, 15672, 16010, 25565, 27017, 27018, 28017, 50000, 50070, 61616,
}

var commonUDP = []int{53, 123, 161}

var scanProfiles = map[string]string{
	"quick":    "Top 100 TCP ports. Discovery, ports and fingerprints.",
	"standard": "Ports 1-1024 plus common high TCP ports, and DNS/NTP/SNMP on UDP. Adds CVE matching and exposure rules.",
	"full":     "All 65,535 TCP ports and DNS/NTP/SNMP on UDP. Same checks as standard.",
	"custom":   "Your own tcp_ports / udp_ports lists or ranges.",
}

type portPlan struct {
	TCP []int
	UDP []int
}

func planForProfile(profile, tcpSpec, udpSpec string) (portPlan, error) {
	switch profile {
	case "", "standard":
		tcp := make([]int, 0, 1100)
		for p := 1; p <= 1024; p++ {
			tcp = append(tcp, p)
		}
		tcp = append(tcp, commonTCP...)
		tcp = append(tcp, extraHighTCP...)
		return portPlan{TCP: uniqueSorted(tcp), UDP: commonUDP}, nil
	case "quick":
		return portPlan{TCP: uniqueSorted(commonTCP)}, nil
	case "full":
		tcp := make([]int, 65535)
		for i := range tcp {
			tcp[i] = i + 1
		}
		return portPlan{TCP: tcp, UDP: commonUDP}, nil
	case "custom":
		tcp, err := parsePortSpec(tcpSpec)
		if err != nil {
			return portPlan{}, fmt.Errorf("tcp_ports: %w", err)
		}
		udp, err := parsePortSpec(udpSpec)
		if err != nil {
			return portPlan{}, fmt.Errorf("udp_ports: %w", err)
		}
		for _, p := range udp {
			if !containsInt(commonUDP, p) {
				return portPlan{}, fmt.Errorf("udp port %d has no probe; supported UDP ports are 53, 123, 161", p)
			}
		}
		if len(tcp)+len(udp) == 0 {
			return portPlan{}, fmt.Errorf("custom profile needs tcp_ports or udp_ports")
		}
		return portPlan{TCP: tcp, UDP: udp}, nil
	default:
		return portPlan{}, fmt.Errorf("unknown profile %q (quick, standard, full, custom)", profile)
	}
}

// parsePortSpec accepts "22,80,443" and ranges like "8000-8100".
func parsePortSpec(spec string) ([]int, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return nil, nil
	}
	var out []int
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		lo, hi := part, part
		if i := strings.Index(part, "-"); i > 0 {
			lo, hi = part[:i], part[i+1:]
		}
		a, err1 := strconv.Atoi(strings.TrimSpace(lo))
		b, err2 := strconv.Atoi(strings.TrimSpace(hi))
		if err1 != nil || err2 != nil || a < 1 || b > 65535 || a > b {
			return nil, fmt.Errorf("invalid port or range %q", part)
		}
		for p := a; p <= b; p++ {
			out = append(out, p)
		}
	}
	return uniqueSorted(out), nil
}

func uniqueSorted(in []int) []int {
	seen := make(map[int]bool, len(in))
	out := make([]int, 0, len(in))
	for _, p := range in {
		if p >= 1 && p <= 65535 && !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	sort.Ints(out)
	return out
}

func containsInt(list []int, v int) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}
