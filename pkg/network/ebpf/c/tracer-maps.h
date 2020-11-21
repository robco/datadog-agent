#ifndef __TRACER_MAPS_H
#define __TRACER_MAPS_H

#include "tracer.h"
#include "bpf_helpers.h"

enum telemetry_counter{tcp_sent_miscounts, missed_tcp_close, udp_send_processed, udp_send_missed};

/* This is a key/value store with the keys being a conn_tuple_t for send & recv calls
 * and the values being conn_stats_ts_t *.
 */
struct bpf_map_def SEC("maps/conn_stats") conn_stats = {
    .type = BPF_MAP_TYPE_HASH,
    .key_size = sizeof(conn_tuple_t),
    .value_size = sizeof(conn_stats_ts_t),
    .max_entries = 0, // This will get overridden at runtime using max_tracked_connections
    .pinning = 0,
    .namespace = "",
};

/* This is a key/value store with the keys being a conn_tuple_t (but without the PID being used)
 * and the values being a tcp_stats_t *.
 */
struct bpf_map_def SEC("maps/tcp_stats") tcp_stats = {
    .type = BPF_MAP_TYPE_HASH,
    .key_size = sizeof(conn_tuple_t),
    .value_size = sizeof(tcp_stats_t),
    .max_entries = 0, // This will get overridden at runtime using max_tracked_connections
    .pinning = 0,
    .namespace = "",
};

/* Will hold the tcp close events
 * The keys are the cpu number and the values a perf file descriptor for a perf event
 */
struct bpf_map_def SEC("maps/tcp_close_event") tcp_close_event = {
    .type = BPF_MAP_TYPE_PERF_EVENT_ARRAY,
    .key_size = sizeof(__u32),
    .value_size = sizeof(__u32),
    .max_entries = 0, // This will get overridden at runtime
    .pinning = 0,
    .namespace = "",
};

/* We use this map as a container for batching closed tcp connections
 * The key represents the CPU core. Ideally we should use a BPF_MAP_TYPE_PERCPU_HASH map
 * or BPF_MAP_TYPE_PERCPU_ARRAY, but they are not available in
 * some of the Kernels we support (4.4 ~ 4.6)
 */
struct bpf_map_def SEC("maps/tcp_close_batch") tcp_close_batch = {
    .type = BPF_MAP_TYPE_HASH,
    .key_size = sizeof(__u32),
    .value_size = sizeof(batch_t),
    .max_entries = 1024,
    .pinning = 0,
    .namespace = "",
};

/* This map is used to match the kprobe & kretprobe of udp_recvmsg */
/* This is a key/value store with the keys being a pid
 * and the values being a struct sock *.
 */
struct bpf_map_def SEC("maps/udp_recv_sock") udp_recv_sock = {
    .type = BPF_MAP_TYPE_HASH,
    .key_size = sizeof(__u64),
    .value_size = sizeof(void*),
    .max_entries = 1024,
    .pinning = 0,
    .namespace = "",
};

/* This maps tracks listening TCP ports. Entries are added to the map via tracing the inet_csk_accept syscall.  The
 * key in the map is the network namespace inode together with the port and the value is a flag that
 * indicates if the port is listening or not. When the socket is destroyed (via tcp_v4_destroy_sock), we set the
 * value to be "port closed" to indicate that the port is no longer being listened on.  We leave the data in place
 * for the userspace side to read and clean up
 */
struct bpf_map_def SEC("maps/port_bindings") port_bindings = {
    .type = BPF_MAP_TYPE_HASH,
    .key_size = sizeof(port_binding_t),
    .value_size = sizeof(__u8),
    .max_entries = 0, // This will get overridden at runtime using max_tracked_connections
    .pinning = 0,
    .namespace = "",
};

/* This behaves the same as port_bindings, except it tracks UDP ports.
 * Key: a port
 * Value: one of PORT_CLOSED, and PORT_OPEN
 */
struct bpf_map_def SEC("maps/udp_port_bindings") udp_port_bindings = {
    .type = BPF_MAP_TYPE_HASH,
    .key_size = sizeof(port_binding_t),
    .value_size = sizeof(__u8),
    .max_entries = 0, // This will get overridden at runtime using max_tracked_connections
    .pinning = 0,
    .namespace = "",
};

/* This is used purely for capturing state between the call and return of the socket() system call.
 * When a sys_socket kprobe fires, we only have access to the params, which can tell us if the socket is using
 * SOCK_DGRAM or not. The kretprobe will only tell us the returned file descriptor.
 *
 * Keys: the PID returned by bpf_get_current_pid_tgid().
 * Value: 1 if the PID is mid-call to socket() and the call is creating a UDP socket, else there will be no entry.
 */
struct bpf_map_def SEC("maps/pending_sockets") pending_sockets = {
    .type = BPF_MAP_TYPE_HASH,
    .key_size = sizeof(__u64),
    .value_size = sizeof(__u8),
    .max_entries = 8192,
    .pinning = 0,
    .namespace = "",
};

/* Similar to pending_sockets this is used for capturing state between the call and return of the bind() system call.
 *
 * Keys: the PId returned by bpf_get_current_pid_tgid()
 * Values: the args of the bind call  being instrumented.
 */
struct bpf_map_def SEC("maps/pending_bind") pending_bind = {
    .type = BPF_MAP_TYPE_HASH,
    .key_size = sizeof(__u64),
    .value_size = sizeof(bind_syscall_args_t),
    .max_entries = 8192,
    .pinning = 0,
    .namespace = "",
};

/* This is written to in the kretprobe for sys_socket to keep track of
 * sockets that were created, but have not yet been bound to a port with
 * sys_bind.
 *
 * Key: a __u64 where the upper 32 bits are the PID of the process which created the socket, and the lower
 * 32 bits are the file descriptor as returned by socket().
 * Value: the values are not relevant. It's only relevant that there is or isn't an entry.
 *
 */
struct bpf_map_def SEC("maps/unbound_sockets") unbound_sockets = {
    .type = BPF_MAP_TYPE_HASH,
    .key_size = sizeof(__u64),
    .value_size = sizeof(__u8),
    .max_entries = 1024,
    .pinning = 0,
    .namespace = "",
};

/* This map is used for telemetry in kernelspace
 * only key 0 is used
 * value is a telemetry object
 */
struct bpf_map_def SEC("maps/telemetry") telemetry = {
    .type = BPF_MAP_TYPE_HASH,
    .key_size = sizeof(__u16),
    .value_size = sizeof(telemetry_t),
    .max_entries = 1,
    .pinning = 0,
    .namespace = "",
};

#endif