# ECS on EC2

Fargate cannot run citron: it does not allow `SYS_ADMIN` or privileged containers.
Use the EC2 launch type with container instances on a cgroup v2 AMI (Amazon Linux 2023).

```sh
aws ecs register-task-definition --cli-input-json file://examples/ecs/task-definition.json
```

## Service

- **Load balancer.** An Application Load Balancer target group with
  `load_balancing.algorithm.type = least_outstanding_requests`, health check path
  `/ready`, and a deregistration delay of 30 seconds so replicas drain before stopping.
- **Idle timeout** on the ALB above `server.write_timeout_seconds` (60s by default).
- **Scaling.** Target tracking on average service CPU.
- **Isolation.** The task is privileged: run it on a dedicated capacity provider, give
  it no task IAM role, and keep the ALB internal.
