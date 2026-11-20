/* SPDX-License-Identifier: (Apache-2.0 OR GPL-2.0-only) */
/*
 * Minimal kernel type definitions for CO-RE.
 *
 * Only the fields netdoctor's programs read are declared. Every struct carries
 * preserve_access_index, so libbpf/cilium-ebpf relocate each field access
 * against the running kernel's BTF at load time: offsets here are irrelevant
 * and the programs run on any kernel whose BTF has these field names.
 */
#ifndef NETDOCTOR_KERNEL_TYPES_H
#define NETDOCTOR_KERNEL_TYPES_H

typedef unsigned char __u8;
typedef unsigned short __u16;
typedef unsigned int __u32;
typedef unsigned long long __u64;
typedef signed char __s8;
typedef short __s16;
typedef int __s32;
typedef long long __s64;
typedef __u16 __be16;
typedef __u32 __be32;
typedef __u32 __wsum;
typedef _Bool bool;
#define true 1
#define false 0

enum {
	BPF_MAP_TYPE_HASH = 1,
	BPF_MAP_TYPE_ARRAY = 2,
	BPF_MAP_TYPE_PERCPU_ARRAY = 6,
	BPF_MAP_TYPE_RINGBUF = 27,
};

enum {
	BPF_ANY = 0,
	BPF_NOEXIST = 1,
};

#pragma clang attribute push(__attribute__((preserve_access_index)), apply_to = record)

struct ns_common {
	unsigned int inum;
};

struct net {
	struct ns_common ns;
};

typedef struct {
	struct net *net;
} possible_net_t;

struct net_device {
	int ifindex;
	possible_net_t nd_net;
};

struct in6_addr {
	union {
		__u8 u6_addr8[16];
	} in6_u;
};

struct sock_common {
	__be32 skc_daddr;
	__be32 skc_rcv_saddr;
	__be16 skc_dport;
	__u16 skc_num;
	unsigned short skc_family;
	volatile unsigned char skc_state;
	possible_net_t skc_net;
	struct in6_addr skc_v6_daddr;
	struct in6_addr skc_v6_rcv_saddr;
};

struct sock {
	struct sock_common __sk_common;
};

struct tcp_sock {
	__u32 srtt_us;
	__u32 total_retrans;
};

struct sk_buff {
	unsigned int len;
	__be16 protocol;
	__u16 transport_header;
	__u16 network_header;
	unsigned char *head;
	unsigned char *data;
	struct net_device *dev;
	struct sock *sk;
	int skb_iif;
};

#pragma clang attribute pop

#endif /* NETDOCTOR_KERNEL_TYPES_H */
