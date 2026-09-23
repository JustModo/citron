# Kubernetes

```sh
kubectl label node <node> citron=true
kubectl taint node <node> citron=true:NoSchedule
kubectl apply -f examples/kubernetes/citron.yaml
```

Push the image to a registry the cluster can pull from and set it in the manifest.

## Notes

- **Privileged pods.** nsjail needs `SYS_ADMIN`, AppArmor unconfined and an unmasked
  `/proc`; `privileged: true` is the portable way to grant all three. Submissions are
  still jailed. Keep the pods on dedicated nodes (the label and taint above) with no
  other workloads or secrets.
- **Load balancing.** A `Service` spreads connections randomly. Put a least-connections
  or latency-aware proxy in front, for example ingress-nginx with
  `nginx.ingress.kubernetes.io/load-balance: ewma`, or kube-proxy in IPVS mode with
  the `lc` scheduler.
- **Autoscaling.** A busy replica is CPU-bound, so a `HorizontalPodAutoscaler` on CPU
  works.
- **Configuration.** To change settings, mount a `ConfigMap` over
  `/opt/citron/configs/citron.conf`. Keys left out keep their defaults.
