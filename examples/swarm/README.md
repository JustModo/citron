# Docker Swarm

```sh
docker build -t citron:latest .        # or push to a registry every node can pull from
docker stack deploy -c examples/swarm/stack.yaml citron
docker service scale citron_citron=5
docker stack rm citron
```

HAProxy uses the same [config](../scaling/haproxy.cfg) as the Compose example.
`endpoint_mode: dnsrr` gives every replica its own DNS record, so HAProxy balances
on connections instead of Swarm's round-robin VIP.

## Notes

- **No user namespace.** Swarm services cannot set `security_opt`, so `/proc` stays
  masked and the kernel refuses a fresh `/proc` inside a nested user namespace.
  [citron.conf](citron.conf) sets `sandbox.user_namespace = false`; submissions still
  run as an unprivileged uid with no capabilities, and `no_new_privs` makes setuid
  binaries inert.
- **Hosts.** Nodes need cgroup v2 and must not apply Docker's default AppArmor profile,
  which forbids mounts: Amazon Linux or RHEL-family hosts work, Ubuntu and Debian with
  AppArmor enabled do not.
- **Published port.** The routing mesh publishes `2358` on every node. Keep the nodes on
  a private network and set `server.auth_token`.
