// SPDX-License-Identifier: (Apache-2.0 OR GPL-2.0-only)
/*
 * netdoctor kernel tracer.
 *
 * All programs are passive BTF-typed tracepoints: they observe and count,
 * never sit in the packet path and never modify packets. Events are emitted
 * only when they match the in-kernel filter (interfaces and/or addresses
 * supplied by userspace), so unrelated traffic costs one map lookup.
 *
 * Programs:
 *   tp_btf/kfree_skb              packet drops with the kernel drop reason
 *   tp_btf/tcp_retransmit_skb     TCP retransmissions (with smoothed RTT)
 *   tp_btf/tcp_receive_reset      TCP RST received
 *   tp_btf/tcp_send_reset         TCP RST sent
 *   tp_btf/inet_sock_set_state    TCP state transitions
 *   tp_btf/netif_receive_skb      per-interface receive counters
 *   tp_btf/net_dev_queue          per-interface transmit counters
 */
#include "kernel_types.h"
#include <bpf/bpf_helpers.h>
#include <bpf/bpf_core_read.h>
#include <bpf/bpf_tracing.h>
#include <bpf/bpf_endian.h>

char LICENSE[] SEC("license") = "Dual BSD/GPL";

#define AF_INET 2
#define AF_INET6 10
#define ETH_P_IP 0x0800
#define ETH_P_IPV6 0x86DD
#define IPPROTO_TCP 6
#define IPPROTO_UDP 17
#define IPPROTO_ICMP 1
#define IPPROTO_ICMPV6 58

/* Event types (keep in sync with internal/ebpf/events.go). */
#define EV_DROP 1
#define EV_TCP_RETRANSMIT 2
#define EV_TCP_RESET_RX 3
#define EV_TCP_RESET_TX 4
#define EV_TCP_STATE 5

/* Event mask bits selecting which programs emit. */
#define MASK_DROPS (1 << 0)
#define MASK_TCP (1 << 1)
#define MASK_IFACE (1 << 2)

/* Counters (percpu array indices). */
#define C_DROP 0
#define C_TCP_RETRANSMIT 1
#define C_TCP_RESET_RX 2
#define C_TCP_RESET_TX 3
#define C_TCP_STATE 4
#define C_RINGBUF_LOST 5
#define C_MAX 6

/* Configuration set by userspace before load. */
const volatile __u32 cfg_event_mask = 0;
const volatile __u32 cfg_filter_ifindex = 0; /* 1: interface filter active */
const volatile __u32 cfg_filter_addr = 0;    /* 1: address filter active */
const volatile __u32 cfg_unfiltered = 0;     /* 1: explicit node-wide tracing */
/*
 * kfree_skb reasons that are not drops (SKB_NOT_DROPPED_YET, SKB_CONSUMED).
 * Their values differ between kernels (on 5.17-6.0 value 1 is NO_SOCKET), so
 * userspace resolves them from kernel BTF; ~0 means "absent".
 */
const volatile __u32 cfg_reason_not_dropped = ~0U;
const volatile __u32 cfg_reason_consumed = ~0U;

struct event {
	__u64 ts_ns;
	__u64 location;
	__u32 type;
	__u32 ifindex;
	__u32 netns;
	__u32 reason;
	__u32 len;
	__u32 srtt_us;
	__u32 pid;
	__u16 family;
	__u16 sport;
	__u16 dport;
	__u8 proto;
	__u8 oldstate;
	__u8 newstate;
	__u8 matched; /* bit0: interface filter, bit1: address filter */
	__u8 pad[2];
	__u8 saddr[16];
	__u8 daddr[16];
};

struct ifkey {
	__u32 netns;
	__u32 ifindex;
};

struct addrkey {
	__u8 addr[16]; /* IPv6, or IPv4-mapped ::ffff:a.b.c.d */
};

struct ifstatkey {
	__u32 netns;
	__u32 ifindex;
	__u32 dir; /* 0 receive, 1 transmit */
};

struct ifstat {
	__u64 packets;
	__u64 bytes;
};

struct {
	__uint(type, BPF_MAP_TYPE_RINGBUF);
	__uint(max_entries, 1 << 20); /* overridden by userspace */
} events SEC(".maps");

struct {
	__uint(type, BPF_MAP_TYPE_HASH);
	__uint(max_entries, 256);
	__type(key, struct ifkey);
	__type(value, __u8);
} filter_ifaces SEC(".maps");

struct {
	__uint(type, BPF_MAP_TYPE_HASH);
	__uint(max_entries, 256);
	__type(key, struct addrkey);
	__type(value, __u8);
} filter_addrs SEC(".maps");

struct {
	__uint(type, BPF_MAP_TYPE_PERCPU_ARRAY);
	__uint(max_entries, C_MAX);
	__type(key, __u32);
	__type(value, __u64);
} counters SEC(".maps");

struct {
	__uint(type, BPF_MAP_TYPE_HASH);
	__uint(max_entries, 1024);
	__type(key, struct ifstatkey);
	__type(value, struct ifstat);
} iface_stats SEC(".maps");

/* Force BTF emission of the event struct for bpf2go. */
const struct event *unused_event __attribute__((unused));

static __always_inline void count(__u32 idx)
{
	__u64 *v = bpf_map_lookup_elem(&counters, &idx);
	if (v)
		*v += 1;
}

static __always_inline __u32 dev_netns(struct net_device *dev)
{
	if (!dev)
		return 0;
	return BPF_CORE_READ(dev, nd_net.net, ns.inum);
}

static __always_inline __u32 sock_netns(struct sock *sk)
{
	if (!sk)
		return 0;
	return BPF_CORE_READ(sk, __sk_common.skc_net.net, ns.inum);
}

static __always_inline void map_v4(__u8 dst[16], __be32 addr)
{
	__builtin_memset(dst, 0, 10);
	dst[10] = 0xff;
	dst[11] = 0xff;
	__builtin_memcpy(&dst[12], &addr, 4);
}

static __always_inline bool addr_match(const __u8 a[16])
{
	struct addrkey k;
	__builtin_memcpy(k.addr, a, 16);
	return bpf_map_lookup_elem(&filter_addrs, &k) != 0;
}

static __always_inline bool iface_match(__u32 netns, __u32 ifindex)
{
	struct ifkey k = { .netns = netns, .ifindex = ifindex };
	return bpf_map_lookup_elem(&filter_ifaces, &k) != 0;
}

/*
 * match returns the matched bits (0 means filtered out). With
 * cfg_unfiltered, everything matches with bits 0 (unattributed).
 */
static __always_inline int match(struct event *e, bool has_iface, bool has_addr)
{
	int bits = 0;
	if (cfg_filter_ifindex && has_iface && iface_match(e->netns, e->ifindex))
		bits |= 1;
	if (cfg_filter_addr && has_addr && (addr_match(e->saddr) || addr_match(e->daddr)))
		bits |= 2;
	if (bits)
		return bits;
	return cfg_unfiltered ? 0x80 : 0;
}

/* Parses L3/L4 headers at l3 into e. Returns true if an IP header was read. */
static __always_inline bool parse_l3(struct event *e, unsigned char *l3)
{
	__u8 vihl = 0;
	if (bpf_probe_read_kernel(&vihl, 1, l3))
		return false;
	__u8 version = vihl >> 4;
	unsigned char *l4 = 0;

	if (version == 4) {
		__be32 saddr, daddr;
		bpf_probe_read_kernel(&e->proto, 1, l3 + 9);
		bpf_probe_read_kernel(&saddr, 4, l3 + 12);
		bpf_probe_read_kernel(&daddr, 4, l3 + 16);
		map_v4(e->saddr, saddr);
		map_v4(e->daddr, daddr);
		e->family = AF_INET;
		l4 = l3 + ((vihl & 0x0f) * 4);
	} else if (version == 6) {
		bpf_probe_read_kernel(&e->proto, 1, l3 + 6);
		bpf_probe_read_kernel(e->saddr, 16, l3 + 8);
		bpf_probe_read_kernel(e->daddr, 16, l3 + 24);
		e->family = AF_INET6;
		l4 = l3 + 40; /* extension headers are not followed */
	} else {
		return false;
	}

	if (e->proto == IPPROTO_TCP || e->proto == IPPROTO_UDP) {
		__be16 ports[2] = {};
		bpf_probe_read_kernel(ports, sizeof(ports), l4);
		e->sport = bpf_ntohs(ports[0]);
		e->dport = bpf_ntohs(ports[1]);
	}
	return true;
}

static __always_inline void fill_from_sock(struct event *e, struct sock *sk)
{
	e->family = BPF_CORE_READ(sk, __sk_common.skc_family);
	e->sport = BPF_CORE_READ(sk, __sk_common.skc_num);
	e->dport = bpf_ntohs(BPF_CORE_READ(sk, __sk_common.skc_dport));
	e->netns = sock_netns(sk);
	e->proto = IPPROTO_TCP;
	if (e->family == AF_INET) {
		map_v4(e->saddr, BPF_CORE_READ(sk, __sk_common.skc_rcv_saddr));
		map_v4(e->daddr, BPF_CORE_READ(sk, __sk_common.skc_daddr));
	} else if (e->family == AF_INET6) {
		BPF_CORE_READ_INTO(&e->saddr, sk, __sk_common.skc_v6_rcv_saddr.in6_u.u6_addr8);
		BPF_CORE_READ_INTO(&e->daddr, sk, __sk_common.skc_v6_daddr.in6_u.u6_addr8);
	}
}

static __always_inline int submit(struct event *tmpl)
{
	struct event *e = bpf_ringbuf_reserve(&events, sizeof(*e), 0);
	if (!e) {
		count(C_RINGBUF_LOST);
		return 0;
	}
	__builtin_memcpy(e, tmpl, sizeof(*e));
	bpf_ringbuf_submit(e, 0);
	return 0;
}

SEC("tp_btf/kfree_skb")
int BPF_PROG(on_kfree_skb, struct sk_buff *skb, void *location, unsigned int reason)
{
	if (!(cfg_event_mask & MASK_DROPS))
		return 0;
	if (reason == cfg_reason_not_dropped || reason == cfg_reason_consumed)
		return 0;

	struct event e = {};
	e.type = EV_DROP;
	e.reason = reason;
	e.location = (__u64)location;
	e.len = BPF_CORE_READ(skb, len);

	struct net_device *dev = BPF_CORE_READ(skb, dev);
	if (dev) {
		e.ifindex = BPF_CORE_READ(dev, ifindex);
		e.netns = dev_netns(dev);
	} else {
		e.ifindex = BPF_CORE_READ(skb, skb_iif);
	}

	bool have_addr = false;
	__u16 nh = BPF_CORE_READ(skb, network_header);
	unsigned char *head = BPF_CORE_READ(skb, head);
	if (nh != (__u16)~0U && head)
		have_addr = parse_l3(&e, head + nh);

	struct sock *sk = BPF_CORE_READ(skb, sk);
	if (!have_addr && sk) {
		fill_from_sock(&e, sk);
		have_addr = true;
	}
	if (!e.netns && sk)
		e.netns = sock_netns(sk);

	int bits = match(&e, e.ifindex != 0, have_addr);
	if (!bits)
		return 0;
	e.matched = bits;
	e.ts_ns = bpf_ktime_get_ns();
	e.pid = bpf_get_current_pid_tgid() >> 32;
	count(C_DROP);
	return submit(&e);
}

static __always_inline int tcp_sock_event(struct sock *sk, __u32 type, __u32 counter)
{
	if (!(cfg_event_mask & MASK_TCP) || !sk)
		return 0;
	struct event e = {};
	e.type = type;
	fill_from_sock(&e, sk);
	e.newstate = BPF_CORE_READ(sk, __sk_common.skc_state);
	if (type == EV_TCP_RETRANSMIT)
		e.srtt_us = BPF_CORE_READ((struct tcp_sock *)sk, srtt_us) >> 3;
	int bits = match(&e, false, true);
	if (!bits)
		return 0;
	e.matched = bits;
	e.ts_ns = bpf_ktime_get_ns();
	e.pid = bpf_get_current_pid_tgid() >> 32;
	count(counter);
	return submit(&e);
}

SEC("tp_btf/tcp_retransmit_skb")
int BPF_PROG(on_tcp_retransmit, struct sock *sk, struct sk_buff *skb)
{
	return tcp_sock_event(sk, EV_TCP_RETRANSMIT, C_TCP_RETRANSMIT);
}

SEC("tp_btf/tcp_receive_reset")
int BPF_PROG(on_tcp_receive_reset, struct sock *sk)
{
	return tcp_sock_event(sk, EV_TCP_RESET_RX, C_TCP_RESET_RX);
}

SEC("tp_btf/tcp_send_reset")
int BPF_PROG(on_tcp_send_reset, struct sock *sk, struct sk_buff *skb)
{
	if (!(cfg_event_mask & MASK_TCP))
		return 0;
	if (sk)
		return tcp_sock_event(sk, EV_TCP_RESET_TX, C_TCP_RESET_TX);
	/* RST for a segment with no socket (e.g. connection to a closed port). */
	if (!skb)
		return 0;
	struct event e = {};
	e.type = EV_TCP_RESET_TX;
	__u16 nh = BPF_CORE_READ(skb, network_header);
	unsigned char *head = BPF_CORE_READ(skb, head);
	if (nh == (__u16)~0U || !head || !parse_l3(&e, head + nh))
		return 0;
	/* The headers are those of the offending segment: swap to describe the RST. */
	__u8 tmp[16];
	__builtin_memcpy(tmp, e.saddr, 16);
	__builtin_memcpy(e.saddr, e.daddr, 16);
	__builtin_memcpy(e.daddr, tmp, 16);
	__u16 p = e.sport;
	e.sport = e.dport;
	e.dport = p;
	struct net_device *dev = BPF_CORE_READ(skb, dev);
	if (dev) {
		e.ifindex = BPF_CORE_READ(dev, ifindex);
		e.netns = dev_netns(dev);
	}
	int bits = match(&e, e.ifindex != 0, true);
	if (!bits)
		return 0;
	e.matched = bits;
	e.ts_ns = bpf_ktime_get_ns();
	count(C_TCP_RESET_TX);
	return submit(&e);
}

SEC("tp_btf/inet_sock_set_state")
int BPF_PROG(on_inet_sock_set_state, struct sock *sk, int oldstate, int newstate)
{
	if (!(cfg_event_mask & MASK_TCP) || !sk)
		return 0;
	struct event e = {};
	e.type = EV_TCP_STATE;
	fill_from_sock(&e, sk);
	e.oldstate = oldstate;
	e.newstate = newstate;
	int bits = match(&e, false, true);
	if (!bits)
		return 0;
	e.matched = bits;
	e.ts_ns = bpf_ktime_get_ns();
	e.pid = bpf_get_current_pid_tgid() >> 32;
	count(C_TCP_STATE);
	return submit(&e);
}

static __always_inline void iface_count(struct sk_buff *skb, __u32 dir)
{
	struct net_device *dev = BPF_CORE_READ(skb, dev);
	if (!dev)
		return;
	struct event e = {};
	e.ifindex = BPF_CORE_READ(dev, ifindex);
	e.netns = dev_netns(dev);

	unsigned char *l3;
	if (dir == 0) {
		/* At netif_receive_skb the MAC header is pulled: data is L3. */
		l3 = BPF_CORE_READ(skb, data);
	} else {
		__u16 nh = BPF_CORE_READ(skb, network_header);
		unsigned char *head = BPF_CORE_READ(skb, head);
		if (nh == (__u16)~0U || !head)
			return;
		l3 = head + nh;
	}
	bool have_addr = l3 && parse_l3(&e, l3);
	int bits = match(&e, true, have_addr);
	if (!bits || bits == 0x80)
		return; /* interface counters are never collected unfiltered */

	struct ifstatkey k = { .netns = e.netns, .ifindex = e.ifindex, .dir = dir };
	struct ifstat *st = bpf_map_lookup_elem(&iface_stats, &k);
	if (!st) {
		struct ifstat zero = {};
		bpf_map_update_elem(&iface_stats, &k, &zero, BPF_NOEXIST);
		st = bpf_map_lookup_elem(&iface_stats, &k);
		if (!st)
			return;
	}
	__sync_fetch_and_add(&st->packets, 1);
	__sync_fetch_and_add(&st->bytes, BPF_CORE_READ(skb, len));
}

SEC("tp_btf/netif_receive_skb")
int BPF_PROG(on_netif_receive_skb, struct sk_buff *skb)
{
	if (cfg_event_mask & MASK_IFACE)
		iface_count(skb, 0);
	return 0;
}

SEC("tp_btf/net_dev_queue")
int BPF_PROG(on_net_dev_queue, struct sk_buff *skb)
{
	if (cfg_event_mask & MASK_IFACE)
		iface_count(skb, 1);
	return 0;
}
